package scim

import (
	"strings"
	"time"
)

// validateUserWrite checks the minimal required fields for a user write.
func validateUserWrite(u User) error {
	if strings.TrimSpace(u.UserName) == "" {
		return NewError(400, "invalidValue", "userName is required")
	}
	return nil
}

// validateGroupWrite checks the minimal required fields for a group write.
func validateGroupWrite(g Group) error {
	if strings.TrimSpace(g.DisplayName) == "" {
		return NewError(400, "invalidValue", "displayName is required")
	}
	return nil
}

// stampUser normalizes schemas, drops the password, and sets meta for a stored
// user. created is preserved across updates; modified marks lastModified.
func stampUser(u User, created, modified time.Time) User {
	u.Password = ""
	u.Schemas = []string{SchemaUser}
	u.Emails = cloneEmails(u.Emails)
	u.Groups = cloneGroupRefs(u.Groups)
	u.Meta = Meta{
		ResourceType: ResourceTypeUser,
		Created:      formatTime(created),
		LastModified: formatTime(modified),
		Location:     usersLocationBase + u.ID,
	}
	u.Meta.Version = userVersion(u)
	return u
}

// stampGroup normalizes schemas and sets meta for a stored group.
func stampGroup(g Group, created, modified time.Time) Group {
	g.Schemas = []string{SchemaGroup}
	g.Members = cloneMemberRefs(g.Members)
	g.Meta = Meta{
		ResourceType: ResourceTypeGroup,
		Created:      formatTime(created),
		LastModified: formatTime(modified),
		Location:     groupsLocationBase + g.ID,
	}
	g.Meta.Version = groupVersion(g)
	return g
}
