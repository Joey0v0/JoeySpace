package model

// Team membership lifecycle is owned by User. Only Active grants team access.
const (
	TeamMembershipActive int8 = iota
	TeamMembershipLeaving
	TeamMembershipLeft
)

// TeamGroupFence is IM's permanent closure boundary for one team/user pair.
// Rejoining a team must never reset or delete this boundary.
type TeamGroupFence struct {
	TeamID                  int64 `gorm:"primaryKey;autoIncrement:false"`
	UserID                  int64 `gorm:"primaryKey;autoIncrement:false"`
	ClosedThroughGeneration int64
}

func (TeamGroupFence) TableName() string { return "im_team_group_fences" }
