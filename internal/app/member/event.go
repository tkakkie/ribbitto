package member

import (
	"encoding/json"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EncodeJoined returns the stored data of realtime.EventMemberJoined, which
// org publishes and so owns (decision 26; here until org's module moves),
// for memberID's join: canonical UUID text under member_id.
func EncodeJoined(memberID domain.ID) ([]byte, error) {
	data, err := json.Marshal(map[string]string{"member_id": realtime.FormatPayloadID(memberID)})
	if err != nil {
		return nil, fmt.Errorf("encoding member.joined data: %w", err)
	}
	return data, nil
}
