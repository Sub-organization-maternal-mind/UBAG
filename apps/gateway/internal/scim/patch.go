package scim

import (
	"encoding/json"
	"strings"
)

// Supported PATCH paths (RFC 7644 §3.5.2) — pragmatic subset.
//
// Users:
//   - active        (replace/add: bool; remove: sets false)
//   - displayName   (replace/add: string; remove: clears)
//   - userName      (replace/add: string)
//   - externalId    (replace/add: string; remove: clears)
//   - emails        (replace/add: []Email or single Email; remove: clears)
//   - "" (no path)  (replace/add: object of the above attributes)
//
// Groups:
//   - displayName   (replace/add: string; remove: clears)
//   - externalId    (replace/add: string; remove: clears)
//   - members       (add: append refs; replace: set refs; remove: clear all)
//   - "" (no path)  (replace/add: object with displayName/externalId/members)

// applyUserPatch applies a sequence of PatchOperations to a user in place.
// It returns a 400-style *Error for unsupported paths or malformed values.
func applyUserPatch(u *User, ops []PatchOperation) error {
	for _, op := range ops {
		opName := strings.ToLower(strings.TrimSpace(op.Op))
		path := strings.TrimSpace(op.Path)
		switch opName {
		case "add", "replace", "remove":
		default:
			return errBadValue("unsupported patch op: " + op.Op)
		}
		if path == "" {
			if opName == "remove" {
				return errBadPath("remove requires a path")
			}
			if err := applyUserNoPath(u, op.Value); err != nil {
				return err
			}
			continue
		}
		if err := applyUserPath(u, opName, strings.ToLower(path), op.Value); err != nil {
			return err
		}
	}
	return nil
}

func applyUserPath(u *User, opName, path string, value json.RawMessage) error {
	switch path {
	case "active":
		if opName == "remove" {
			u.Active = false
			return nil
		}
		var b bool
		if err := json.Unmarshal(value, &b); err != nil {
			return errBadValue("active must be a boolean")
		}
		u.Active = b
	case "displayname":
		return applyUserString(&u.DisplayName, opName, value, "displayName")
	case "username":
		if opName == "remove" {
			return errBadValue("userName is required and cannot be removed")
		}
		return applyUserString(&u.UserName, opName, value, "userName")
	case "externalid":
		return applyUserString(&u.ExternalID, opName, value, "externalId")
	case "emails":
		if opName == "remove" {
			u.Emails = nil
			return nil
		}
		emails, err := decodeEmails(value)
		if err != nil {
			return err
		}
		if opName == "add" {
			u.Emails = append(u.Emails, emails...)
			return nil
		}
		u.Emails = emails
	default:
		return errBadPath("unsupported user patch path: " + path)
	}
	return nil
}

func applyUserString(target *string, opName string, value json.RawMessage, attr string) error {
	if opName == "remove" {
		*target = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return errBadValue(attr + " must be a string")
	}
	*target = s
	return nil
}

// applyUserNoPath applies a path-less add/replace whose value is an object of
// user attributes (RFC 7644 §3.5.2.1).
func applyUserNoPath(u *User, value json.RawMessage) error {
	if len(value) == 0 {
		return errBadValue("patch value object is required when path is omitted")
	}
	var patch struct {
		Active      *bool   `json:"active"`
		DisplayName *string `json:"displayName"`
		UserName    *string `json:"userName"`
		ExternalID  *string `json:"externalId"`
		Emails      []Email `json:"emails"`
	}
	if err := json.Unmarshal(value, &patch); err != nil {
		return errBadValue("patch value must be a JSON object")
	}
	if patch.Active != nil {
		u.Active = *patch.Active
	}
	if patch.DisplayName != nil {
		u.DisplayName = *patch.DisplayName
	}
	if patch.UserName != nil {
		u.UserName = *patch.UserName
	}
	if patch.ExternalID != nil {
		u.ExternalID = *patch.ExternalID
	}
	if patch.Emails != nil {
		u.Emails = patch.Emails
	}
	return nil
}

func decodeEmails(value json.RawMessage) ([]Email, error) {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		return nil, errBadValue("emails value is required")
	}
	if strings.HasPrefix(trimmed, "[") {
		var emails []Email
		if err := json.Unmarshal(value, &emails); err != nil {
			return nil, errBadValue("emails must be an array of email objects")
		}
		return emails, nil
	}
	var single Email
	if err := json.Unmarshal(value, &single); err != nil {
		return nil, errBadValue("emails must be an email object or array")
	}
	return []Email{single}, nil
}

// applyGroupPatch applies a sequence of PatchOperations to a group in place.
func applyGroupPatch(g *Group, ops []PatchOperation) error {
	for _, op := range ops {
		opName := strings.ToLower(strings.TrimSpace(op.Op))
		path := strings.TrimSpace(op.Path)
		switch opName {
		case "add", "replace", "remove":
		default:
			return errBadValue("unsupported patch op: " + op.Op)
		}
		if path == "" {
			if opName == "remove" {
				return errBadPath("remove requires a path")
			}
			if err := applyGroupNoPath(g, op.Value); err != nil {
				return err
			}
			continue
		}
		if err := applyGroupPath(g, opName, strings.ToLower(path), op.Value); err != nil {
			return err
		}
	}
	return nil
}

func applyGroupPath(g *Group, opName, path string, value json.RawMessage) error {
	switch path {
	case "displayname":
		if opName == "remove" {
			return errBadValue("displayName is required and cannot be removed")
		}
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return errBadValue("displayName must be a string")
		}
		g.DisplayName = s
	case "externalid":
		if opName == "remove" {
			g.ExternalID = ""
			return nil
		}
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return errBadValue("externalId must be a string")
		}
		g.ExternalID = s
	case "members":
		if opName == "remove" {
			g.Members = nil
			return nil
		}
		members, err := decodeMembers(value)
		if err != nil {
			return err
		}
		if opName == "add" {
			g.Members = append(g.Members, members...)
			return nil
		}
		g.Members = members
	default:
		return errBadPath("unsupported group patch path: " + path)
	}
	return nil
}

func applyGroupNoPath(g *Group, value json.RawMessage) error {
	if len(value) == 0 {
		return errBadValue("patch value object is required when path is omitted")
	}
	var patch struct {
		DisplayName *string     `json:"displayName"`
		ExternalID  *string     `json:"externalId"`
		Members     []MemberRef `json:"members"`
	}
	if err := json.Unmarshal(value, &patch); err != nil {
		return errBadValue("patch value must be a JSON object")
	}
	if patch.DisplayName != nil {
		g.DisplayName = *patch.DisplayName
	}
	if patch.ExternalID != nil {
		g.ExternalID = *patch.ExternalID
	}
	if patch.Members != nil {
		g.Members = patch.Members
	}
	return nil
}

func decodeMembers(value json.RawMessage) ([]MemberRef, error) {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		return nil, errBadValue("members value is required")
	}
	if strings.HasPrefix(trimmed, "[") {
		var members []MemberRef
		if err := json.Unmarshal(value, &members); err != nil {
			return nil, errBadValue("members must be an array of member objects")
		}
		return members, nil
	}
	var single MemberRef
	if err := json.Unmarshal(value, &single); err != nil {
		return nil, errBadValue("members must be a member object or array")
	}
	return []MemberRef{single}, nil
}
