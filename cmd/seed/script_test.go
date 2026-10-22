package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
)

func scriptWith(name, handle string, bodies ...string) script {
	var data script
	data.Members = append(data.Members, struct{ Name, Handle string }{name, handle})
	channel := struct {
		Name     string
		Messages []scriptMessage
	}{Name: "general"}
	for _, body := range bodies {
		channel.Messages = append(channel.Messages, scriptMessage{Author: handle, Body: body})
	}
	data.Channels = append(data.Channels, channel)
	return data
}

func TestCheckScript(t *testing.T) {
	for _, tt := range []struct {
		name string
		data script
		want string // text in the error, empty when the script is accepted
	}{
		{"valid", scriptWith("Mira Vale", "mira", "hello"), ""},
		{"longest body", scriptWith("Mira Vale", "mira", strings.Repeat("a", conversation.MaxMessageBodyLength)), ""},
		{"body over the limit", scriptWith("Mira Vale", "mira", "hello", strings.Repeat("a", conversation.MaxMessageBodyLength+1)), "message 2 is over 4000"},
		{"blank display name", scriptWith("   ", "mira", "hello"), "display name"},
		{"display name over the limit", scriptWith(strings.Repeat("a", 51), "mira", "hello"), "display name"},
		{"reserved handle", scriptWith("Mira Vale", "everyone", "hello"), "reserved"},
		{"channel without messages", scriptWith("Mira Vale", "mira"), "has no messages"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkScript(tt.data)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("checkScript = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestEmbeddedScriptIsAccepted(t *testing.T) {
	var data script
	if err := json.Unmarshal(conversations, &data); err != nil {
		t.Fatal(err)
	}
	if err := checkScript(data); err != nil {
		t.Fatalf("conversations.json: %v", err)
	}
}
