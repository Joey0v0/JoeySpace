package main

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A membership generation is accepted only from User's authenticated response.
// Missing fields from an older User service must never default to generation 1.
func validateTeamGroupAuthorization(userID, authorizedUserID, generation int64) error {
	if userID <= 0 || authorizedUserID != userID || generation <= 0 {
		return status.Error(codes.Unavailable, "team membership authorization is invalid")
	}
	return nil
}
