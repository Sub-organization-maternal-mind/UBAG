package jitadmin

import (
	"testing"
	"time"
)

func TestGrantIsActive(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name string
		g    Grant
		now  time.Time
		want bool
	}{
		{
			name: "approved and unexpired",
			g:    Grant{Approved: true, ExpiresAt: future, Revoked: false},
			now:  now,
			want: true,
		},
		{
			name: "expired",
			g:    Grant{Approved: true, ExpiresAt: past, Revoked: false},
			now:  now,
			want: false,
		},
		{
			name: "not approved",
			g:    Grant{Approved: false, ExpiresAt: future, Revoked: false},
			now:  now,
			want: false,
		},
		{
			name: "revoked",
			g:    Grant{Approved: true, ExpiresAt: future, Revoked: true},
			now:  now,
			want: false,
		},
		{
			name: "revoked and expired",
			g:    Grant{Approved: true, ExpiresAt: past, Revoked: true},
			now:  now,
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := tc.g.IsActive(tc.now)
			if got != tc.want {
				t.Errorf("IsActive() = %v, want %v", got, tc.want)
			}
		})
	}
}
