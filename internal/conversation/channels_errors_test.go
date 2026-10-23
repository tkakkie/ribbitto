package conversation_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type channelsErrStore struct {
	channelStore
	defaultErr, lockErr, createErr error
	defaultOrgs, lockOrgs          []kernel.ID
	createOrgs                     []kernel.ID
	calls                          []string
}

func (s *channelsErrStore) GetDefaultChannel(ctx context.Context, organizationID kernel.ID) (conversation.Channel, error) {
	s.calls = append(s.calls, "default")
	s.defaultOrgs = append(s.defaultOrgs, organizationID)
	if s.defaultErr != nil {
		return conversation.Channel{}, s.defaultErr
	}
	return s.channelStore.GetDefaultChannel(ctx, organizationID)
}

func (s *channelsErrStore) LockCreation(ctx context.Context, organizationID kernel.ID) error {
	s.calls = append(s.calls, "lock")
	s.lockOrgs = append(s.lockOrgs, organizationID)
	if s.lockErr != nil {
		return s.lockErr
	}
	return s.channelStore.LockCreation(ctx, organizationID)
}

func (s *channelsErrStore) CreateChannel(ctx context.Context, organizationID kernel.ID, name, image string) (conversation.Channel, error) {
	s.calls = append(s.calls, "create")
	s.createOrgs = append(s.createOrgs, organizationID)
	if s.createErr != nil {
		return conversation.Channel{}, s.createErr
	}
	return s.channelStore.CreateChannel(ctx, organizationID, name, image)
}

func TestChannelsErrors(t *testing.T) {
	acmeOrgID, decoyOrgID := kernel.ID{1}, kernel.ID{2}
	generalChannelID, decoyChannelID := kernel.ID{3}, kernel.ID{4}
	generalTopicID, decoyTopicID := kernel.ID{5}, kernel.ID{6}
	memberID := kernel.ID{9}
	createdChannelID := kernel.ID{10} // channelStore assigns len(created)+10 to the first created channel.

	general := conversation.Channel{
		ID: generalChannelID, OrganizationID: acmeOrgID, DefaultTopicID: generalTopicID,
		Name: "general", IsDefault: true,
	}
	decoyDefault := conversation.Channel{
		ID: decoyChannelID, OrganizationID: decoyOrgID, DefaultTopicID: decoyTopicID,
		Name: "secret", IsDefault: true,
	}
	wantCreated := conversation.Channel{
		ID: createdChannelID, OrganizationID: acmeOrgID, Name: "design",
	}
	// Member.OrganizationID carries decoyOrgID so reading it instead of
	// Organization.ID crosses into the decoy organisation.
	membership := org.Membership{
		Organization: org.Organization{ID: acmeOrgID},
		Member:       org.Member{ID: memberID, OrganizationID: decoyOrgID},
	}
	failure := errors.New("channel store failed")

	for _, tt := range []struct {
		name                           string
		create, missingDefault         bool
		defaultErr, lockErr, createErr error
		wantChannel                    conversation.Channel
		wantCreated                    []conversation.Channel
		wantErr, forbidErr             error
		wantError                      string
		wantCalls, wantRunnerCalls     []string
		wantDefaultOrgs                []kernel.ID
		wantLockOrgs, wantCreateOrgs   []kernel.ID
	}{
		{
			name:            "default forwards organisation ID",
			wantChannel:     general,
			wantCalls:       []string{"default"},
			wantDefaultOrgs: []kernel.ID{acmeOrgID},
		},
		{
			name:            "missing default is not ErrChannelNotFound and names organisation",
			missingDefault:  true,
			forbidErr:       conversation.ErrChannelNotFound,
			wantError:       fmt.Sprintf("organisation %x has no default channel", acmeOrgID),
			wantCalls:       []string{"default"},
			wantDefaultOrgs: []kernel.ID{acmeOrgID},
		},
		{
			name:            "default store error is wrapped",
			defaultErr:      failure,
			wantErr:         failure,
			wantError:       "finding default channel: " + failure.Error(),
			wantCalls:       []string{"default"},
			wantDefaultOrgs: []kernel.ID{acmeOrgID},
		},
		{
			name:            "create forwards organisation ID",
			create:          true,
			wantChannel:     wantCreated,
			wantCreated:     []conversation.Channel{wantCreated},
			wantCalls:       []string{"lock", "create"},
			wantRunnerCalls: []string{"begin", "commit"},
			wantLockOrgs:    []kernel.ID{acmeOrgID},
			wantCreateOrgs:  []kernel.ID{acmeOrgID},
		},
		{
			name:            "lock creation error is wrapped and stops before create",
			create:          true,
			lockErr:         failure,
			wantErr:         failure,
			wantError:       "creating channel: " + failure.Error(),
			wantCalls:       []string{"lock"},
			wantRunnerCalls: []string{"begin"},
			wantLockOrgs:    []kernel.ID{acmeOrgID},
		},
		{
			name:            "create channel error is wrapped",
			create:          true,
			createErr:       failure,
			wantErr:         failure,
			wantError:       "creating channel: " + failure.Error(),
			wantCalls:       []string{"lock", "create"},
			wantRunnerCalls: []string{"begin"},
			wantLockOrgs:    []kernel.ID{acmeOrgID},
			wantCreateOrgs:  []kernel.ID{acmeOrgID},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			channels := map[kernel.ID][]conversation.Channel{
				acmeOrgID:  {general},
				decoyOrgID: {decoyDefault},
			}
			if tt.missingDefault {
				channels = nil
			}
			store := &channelsErrStore{
				channelStore: channelStore{channels: channels},
				defaultErr:   tt.defaultErr,
				lockErr:      tt.lockErr,
				createErr:    tt.createErr,
			}
			runner := &postingFake{t: t}
			service := conversation.NewTransactionalChannels(store, runner, func(tx platform.Tx) conversation.ChannelCreator {
				runner.bound(tx)
				return store
			})

			var (
				got conversation.Channel
				err error
			)
			if tt.create {
				got, err = service.Create(t.Context(), membership, "  design ")
			} else {
				got, err = service.Default(t.Context(), membership)
			}

			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Errorf("error = %v, want substring %q", err, tt.wantError)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Errorf("error %v does not wrap %v", err, tt.wantErr)
				}
				if tt.forbidErr != nil && errors.Is(err, tt.forbidErr) {
					t.Errorf("error %v must not wrap %v", err, tt.forbidErr)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if got != tt.wantChannel {
				t.Errorf("channel = %+v, want %+v", got, tt.wantChannel)
			}
			if !slices.Equal(store.created, tt.wantCreated) {
				t.Errorf("created = %+v, want %+v", store.created, tt.wantCreated)
			}
			if !slices.Equal(store.calls, tt.wantCalls) {
				t.Errorf("store calls = %v, want %v", store.calls, tt.wantCalls)
			}
			if !slices.Equal(runner.calls, tt.wantRunnerCalls) {
				t.Errorf("runner calls = %v, want %v", runner.calls, tt.wantRunnerCalls)
			}
			if !slices.Equal(store.defaultOrgs, tt.wantDefaultOrgs) {
				t.Errorf("GetDefaultChannel organisations = %v, want %v", store.defaultOrgs, tt.wantDefaultOrgs)
			}
			if !slices.Equal(store.lockOrgs, tt.wantLockOrgs) {
				t.Errorf("LockCreation organisations = %v, want %v", store.lockOrgs, tt.wantLockOrgs)
			}
			if !slices.Equal(store.createOrgs, tt.wantCreateOrgs) {
				t.Errorf("CreateChannel organisations = %v, want %v", store.createOrgs, tt.wantCreateOrgs)
			}
		})
	}
}
