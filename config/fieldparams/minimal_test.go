//go:build minimal

package field_params_test

import (
	"testing"

	fieldparams "github.com/prysmaticlabs/prysm/v5/config/fieldparams"
	"github.com/prysmaticlabs/prysm/v5/config/params"
	"github.com/prysmaticlabs/prysm/v5/testing/require"
)

func TestFieldParametersValues(t *testing.T) {
	params.SetupTestConfigCleanup(t)
	min := params.MinimalSpecConfig().Copy()
	params.OverrideBeaconConfig(min)
	require.Equal(t, "minimal", fieldparams.Preset)
	testFieldParametersMatchConfig(t)
}
