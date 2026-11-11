package main

// Group administration endpoints. Like /api/send and /api/download they are
// wrapped in localOnly.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type groupRequest struct {
	JID          string   `json:"jid"`
	Name         string   `json:"name"`
	Action       string   `json:"action"`
	Participants []string `json:"participants"`
}

type groupParticipantOut struct {
	JID          string `json:"jid"`
	Phone        string `json:"phone"`
	IsAdmin      bool   `json:"is_admin"`
	IsSuperAdmin bool   `json:"is_super_admin"`
	Error        int    `json:"error,omitempty"`
}

type groupOut struct {
	JID              string                `json:"jid"`
	Name             string                `json:"name"`
	ParticipantCount int                   `json:"participant_count"`
	Participants     []groupParticipantOut `json:"participants,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// parseUserJID accepts a full JID or a phone number with country code (only digits count).
func parseUserJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "@") {
		return types.ParseJID(s)
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(digits) < 8 {
		return types.JID{}, fmt.Errorf("invalid phone number: %q (include the country code, e.g. 39...)", s)
	}
	return types.NewJID(digits, types.DefaultUserServer), nil
}

func parseGroupJID(s string) (types.JID, error) {
	jid, err := types.ParseJID(strings.TrimSpace(s))
	if err != nil {
		return jid, err
	}
	if jid.Server != types.GroupServer {
		return jid, fmt.Errorf("%q is not a group JID (it must end in @g.us)", s)
	}
	return jid, nil
}

func participantsOut(ps []types.GroupParticipant) []groupParticipantOut {
	out := make([]groupParticipantOut, 0, len(ps))
	for _, p := range ps {
		phone := ""
		if !p.PhoneNumber.IsEmpty() {
			phone = p.PhoneNumber.User
		} else if p.JID.Server == types.DefaultUserServer {
			phone = p.JID.User
		}
		out = append(out, groupParticipantOut{
			JID: p.JID.String(), Phone: phone,
			IsAdmin: p.IsAdmin, IsSuperAdmin: p.IsSuperAdmin, Error: p.Error,
		})
	}
	return out
}

func groupToOut(g *types.GroupInfo, withParticipants bool) groupOut {
	o := groupOut{JID: g.JID.String(), Name: g.Name, ParticipantCount: len(g.Participants)}
	if g.ParticipantCount > o.ParticipantCount {
		o.ParticipantCount = g.ParticipantCount
	}
	if withParticipants {
		o.Participants = participantsOut(g.Participants)
	}
	return o
}

func registerGroupHandlers(client *whatsmeow.Client) {
	handle := func(path string, fn func(context.Context, groupRequest) (any, error)) {
		http.HandleFunc(path, localOnly(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var req groupRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "Invalid request format"})
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := fn(ctx, req)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": out})
		}))
	}

	handle("/api/groups/list", func(ctx context.Context, _ groupRequest) (any, error) {
		groups, err := client.GetJoinedGroups(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]groupOut, 0, len(groups))
		for _, g := range groups {
			out = append(out, groupToOut(g, false))
		}
		return out, nil
	})

	handle("/api/groups/info", func(ctx context.Context, req groupRequest) (any, error) {
		jid, err := parseGroupJID(req.JID)
		if err != nil {
			return nil, err
		}
		info, err := client.GetGroupInfo(ctx, jid)
		if err != nil {
			return nil, err
		}
		return groupToOut(info, true), nil
	})

	handle("/api/groups/rename", func(ctx context.Context, req groupRequest) (any, error) {
		jid, err := parseGroupJID(req.JID)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return nil, fmt.Errorf("the new name is empty")
		}
		if err := client.SetGroupName(ctx, jid, name); err != nil {
			return nil, err
		}
		return map[string]string{"jid": jid.String(), "name": name}, nil
	})

	handle("/api/groups/create", func(ctx context.Context, req groupRequest) (any, error) {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return nil, fmt.Errorf("a group name is required")
		}
		jids := make([]types.JID, 0, len(req.Participants))
		for _, p := range req.Participants {
			j, err := parseUserJID(p)
			if err != nil {
				return nil, err
			}
			jids = append(jids, j)
		}
		info, err := client.CreateGroup(ctx, whatsmeow.ReqCreateGroup{Name: name, Participants: jids})
		if err != nil {
			return nil, err
		}
		return groupToOut(info, true), nil
	})

	handle("/api/groups/participants", func(ctx context.Context, req groupRequest) (any, error) {
		jid, err := parseGroupJID(req.JID)
		if err != nil {
			return nil, err
		}
		var action whatsmeow.ParticipantChange
		switch strings.ToLower(strings.TrimSpace(req.Action)) {
		case "add":
			action = whatsmeow.ParticipantChangeAdd
		case "remove":
			action = whatsmeow.ParticipantChangeRemove
		case "promote":
			action = whatsmeow.ParticipantChangePromote
		case "demote":
			action = whatsmeow.ParticipantChangeDemote
		default:
			return nil, fmt.Errorf("invalid action %q: use add, remove, promote or demote", req.Action)
		}
		if len(req.Participants) == 0 {
			return nil, fmt.Errorf("no participants given")
		}
		jids := make([]types.JID, 0, len(req.Participants))
		for _, p := range req.Participants {
			j, err := parseUserJID(p)
			if err != nil {
				return nil, err
			}
			jids = append(jids, j)
		}
		res, err := client.UpdateGroupParticipants(ctx, jid, jids, action)
		if err != nil {
			return nil, err
		}
		return participantsOut(res), nil
	})

	handle("/api/groups/invite_link", func(ctx context.Context, req groupRequest) (any, error) {
		jid, err := parseGroupJID(req.JID)
		if err != nil {
			return nil, err
		}
		// reset=false: return the current link without invalidating the ones already sent.
		link, err := client.GetGroupInviteLink(ctx, jid, false)
		if err != nil {
			return nil, err
		}
		return map[string]string{"jid": jid.String(), "link": link}, nil
	})
}
