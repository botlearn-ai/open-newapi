package common

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestRelayInfoResetStreamAttemptState(t *testing.T) {
	info := &RelayInfo{
		StartTime:             time.Now().Add(-time.Second),
		FirstResponseTime:     time.Now(),
		isFirstResponse:       false,
		SendResponseCount:     2,
		ReceivedResponseCount: 3,
		StreamStatus:          NewStreamStatus(),
	}

	info.ResetStreamAttemptState()

	require.True(t, info.FirstResponseTime.IsZero())
	require.True(t, info.isFirstResponse)
	require.Zero(t, info.SendResponseCount)
	require.Zero(t, info.ReceivedResponseCount)
	require.Nil(t, info.StreamStatus)

	info.SetFirstResponseTime()
	require.False(t, info.FirstResponseTime.IsZero())
}

func TestRelayInfoGetFinalRequestRelayFormatPrefersExplicitFinal(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		RequestConversionChain:  []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		FinalRequestRelayFormat: types.RelayFormatOpenAIResponses,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToConversionChain(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatClaude), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToRelayFormat(t *testing.T) {
	info := &RelayInfo{
		RelayFormat: types.RelayFormatGemini,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatGemini), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatNilReceiver(t *testing.T) {
	var info *RelayInfo
	require.Equal(t, types.RelayFormat(""), info.GetFinalRequestRelayFormat())
}
