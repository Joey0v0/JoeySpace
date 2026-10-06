package main

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateTeamGroupAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		user, authorized, generation int64
		want                         codes.Code
	}{
		{"current", 42, 42, 1, codes.OK},
		{"exact large version", 42, 42, 9007199254740993, codes.OK},
		{"missing response", 42, 0, 0, codes.Unavailable},
		{"other user", 42, 43, 1, codes.Unavailable},
		{"missing version", 42, 42, 0, codes.Unavailable},
		{"negative version", 42, 42, -1, codes.Unavailable},
		{"invalid caller", 0, 0, 1, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateTeamGroupAuthorization(tc.user, tc.authorized, tc.generation); status.Code(err) != tc.want {
				t.Fatalf("authorization: %v, want %v", err, tc.want)
			}
		})
	}
}
