package identity

import "testing"

func TestACRSatisfies(t *testing.T) {
	tests := []struct {
		have      string
		requested []string
		want      bool
	}{
		{acrMFA, nil, true},
		{acrMFA, []string{acrMFA}, true},
		{acrMFA, []string{acrPhishingResistant}, false},
		{acrPhishingResistant, []string{acrPhishingResistant}, true},
		{acrPhishingResistant, []string{acrPhishingResistantHardware}, false},
		{acrPhishingResistantHardware, []string{acrPhishingResistant}, true},
		{acrMFA, []string{acrPhishingResistantHardware, acrMFA}, true}, // any listed value suffices
		{acrMFA, []string{"urn:unknown"}, true},                        // unknown values are ignored
		{"", []string{acrMFA}, false},
	}
	for _, tt := range tests {
		if got := acrSatisfies(tt.have, tt.requested); got != tt.want {
			t.Errorf("acrSatisfies(%q, %v) = %v, want %v", tt.have, tt.requested, got, tt.want)
		}
	}
}
