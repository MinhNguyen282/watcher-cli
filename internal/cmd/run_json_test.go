package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestRunStreamJSONEmitsNDJSON(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", out: buf, read: map[string]int{}, json: true}

	_, err := s.handle(pubsub.Event[proto.Message]{Payload: proto.Message{
		ID: "m1", SessionID: "S", Role: proto.Assistant,
		Parts: []proto.ContentPart{proto.TextContent{Text: "hello"}},
	}}, nil)
	require.NoError(t, err)

	done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S",
		MessageID: "m1",
		Text:      "hello",
	}}, nil)
	require.NoError(t, err)
	require.True(t, done)

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.Len(t, lines, 2)
	for _, line := range lines {
		var object map[string]any
		require.NoError(t, json.Unmarshal(line, &object), "stdout must be one JSON object per line")
	}
	require.Contains(t, string(lines[1]), `"status":"completed"`)
}
