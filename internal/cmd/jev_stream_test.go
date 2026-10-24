package cmd

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestRunStream_JevObservationDoesNotTerminate(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", runID: "R", out: buf, read: map[string]int{}}
	done, err := s.handle(pubsub.Event[proto.AgentEvent]{Payload: proto.AgentEvent{
		Type: "routing_observation", SessionID: "S", RunID: "R", RoutingSummary: "Jev observe: debug",
	}}, func() { t.Fatal("Observation must not stop the stream") })
	require.NoError(t, err)
	require.False(t, done)
	require.Empty(t, buf.String(), "Observation must not enter assistant stdout")
}
