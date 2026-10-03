package member

import (
	"encoding/json"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// KindJoined is the kind of a member joining an organisation, which org
// publishes and so owns (decision 26; here until org's module moves).
const KindJoined realtime.EventKind = "member.joined"

// Joined is the decoded payload of KindJoined.
type Joined struct {
	MemberID domain.ID
}

// EncodeJoined returns the stored data of KindJoined for memberID's join:
// canonical UUID text under member_id.
func EncodeJoined(memberID domain.ID) ([]byte, error) {
	data, err := json.Marshal(map[string]string{"member_id": realtime.FormatPayloadID(memberID)})
	if err != nil {
		return nil, fmt.Errorf("encoding member.joined data: %w", err)
	}
	return data, nil
}

// DecodeJoined reads stored data; member_id is required.
func DecodeJoined(data []byte) (Joined, error) {
	var raw struct {
		MemberID string `json:"member_id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Joined{}, err
	}
	id, err := realtime.ParsePayloadID(raw.MemberID)
	if err != nil {
		return Joined{}, err
	}
	return Joined{MemberID: id}, nil
}

// RouteJoined is member.joined's realtime.Router. A join belongs to no
// channel, so streams never deliver it; the payload is still validated.
func RouteJoined(payload []byte) (domain.ID, []domain.ID, error) {
	_, err := DecodeJoined(payload)
	return domain.ID{}, nil, err
}
