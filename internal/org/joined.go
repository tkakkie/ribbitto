package org

import (
	"encoding/json"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// KindJoined is the kind of a member joining an organisation, which org
// publishes and so owns (decision 26).
const KindJoined realtime.EventKind = "member.joined"

// Joined is the decoded payload of KindJoined.
type Joined struct {
	MemberID kernel.ID
}

// EncodeJoined returns the stored data of KindJoined for memberID's join:
// canonical UUID text under member_id.
func EncodeJoined(memberID kernel.ID) []byte {
	// This payload contains only strings, so marshaling cannot fail.
	data, _ := json.Marshal(map[string]string{"member_id": realtime.FormatPayloadID(memberID)})
	return data
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
func RouteJoined(payload []byte) (kernel.ID, []kernel.ID, error) {
	_, err := DecodeJoined(payload)
	return kernel.ID{}, nil, err
}
