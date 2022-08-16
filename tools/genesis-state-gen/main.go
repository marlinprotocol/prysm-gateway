package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/io/file"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/v3/runtime/interop"
	log "github.com/sirupsen/logrus"
)

// DepositDataJSON representing a json object of hex string and uint64 values for
// validators on Ethereum. This file can be generated using the official eth2.0-deposit-cli.
type DepositDataJSON struct {
	PubKey                string `json:"pubkey"`
	Amount                uint64 `json:"amount"`
	WithdrawalCredentials string `json:"withdrawal_credentials"`
	DepositDataRoot       string `json:"deposit_data_root"`
	Signature             string `json:"signature"`
}

var (
	depositJSONFile = flag.String(
		"deposit-json-file",
		"",
		"Path to deposit_data.json file generated by the eth2.0-deposit-cli tool",
	)
	numValidators    = flag.Int("num-validators", 0, "Number of validators to deterministically generate in the generated genesis state")
	useMainnetConfig = flag.Bool("mainnet-config", false, "Select whether genesis state should be generated with mainnet or minimal (default) params")
	genesisTime      = flag.Uint64("genesis-time", 0, "Unix timestamp used as the genesis time in the generated genesis state (defaults to now)")
	sszOutputFile    = flag.String("output-ssz", "", "Output filename of the SSZ marshaling of the generated genesis state")
	yamlOutputFile   = flag.String("output-yaml", "", "Output filename of the YAML marshaling of the generated genesis state")
	jsonOutputFile   = flag.String("output-json", "", "Output filename of the JSON marshaling of the generated genesis state")
	configName       = flag.String("config-name", params.MinimalName, "ConfigName for the BeaconChainConfig that will be used for interop, inc GenesisForkVersion of generated genesis state")
)

func main() {
	flag.Parse()
	if *genesisTime == 0 {
		log.Print("No --genesis-time specified, defaulting to now")
	}
	if *sszOutputFile == "" && *yamlOutputFile == "" && *jsonOutputFile == "" {
		log.Println("Expected --output-ssz, --output-yaml, or --output-json to have been provided, received nil")
		return
	}
	if *useMainnetConfig {
		if err := params.SetActive(params.MainnetConfig().Copy()); err != nil {
			log.Fatalf("unable to set mainnet config active, err=%s", err.Error())
		}
	} else {
		cfg, err := params.ByName(*configName)
		if err != nil {
			log.Fatalf("unable to find config using name %s, err=%s", *configName, err.Error())
		}
		if err := params.SetActive(cfg.Copy()); err != nil {
			log.Fatalf("unable to set %s config active, err=%s", cfg.ConfigName, err.Error())
		}
	}
	var genesisState *ethpb.BeaconState
	var err error
	if *depositJSONFile != "" {
		inputFile := *depositJSONFile
		expanded, err := file.ExpandPath(inputFile)
		if err != nil {
			log.WithError(err).Printf("Could not expand file path %s", inputFile)
			return
		}
		inputJSON, err := os.Open(expanded) // #nosec G304
		if err != nil {
			log.WithError(err).Print("Could not open JSON file for reading")
			return
		}
		defer func() {
			if err := inputJSON.Close(); err != nil {
				log.WithError(err).Printf("Could not close file %s", inputFile)
			}
		}()
		log.Printf("Generating genesis state from input JSON deposit data %s", inputFile)
		genesisState, err = genesisStateFromJSONValidators(inputJSON, *genesisTime)
		if err != nil {
			log.WithError(err).Print("Could not generate genesis beacon state")
			return
		}
	} else {
		if *numValidators == 0 {
			log.Println("Expected --num-validators to have been provided, received 0")
			return
		}
		// If no JSON input is specified, we create the state deterministically from interop keys.
		genesisState, _, err = interop.GenerateGenesisState(context.Background(), *genesisTime, uint64(*numValidators))
		if err != nil {
			log.WithError(err).Print("Could not generate genesis beacon state")
			return
		}
	}

	if *sszOutputFile != "" {
		encodedState, err := genesisState.MarshalSSZ()
		if err != nil {
			log.WithError(err).Print("Could not ssz marshal the genesis beacon state")
			return
		}
		if err := file.WriteFile(*sszOutputFile, encodedState); err != nil {
			log.WithError(err).Print("Could not write encoded genesis beacon state to file")
			return
		}
		log.Printf("Done writing to %s", *sszOutputFile)
	}
	if *yamlOutputFile != "" {
		encodedState, err := yaml.Marshal(genesisState)
		if err != nil {
			log.WithError(err).Print("Could not yaml marshal the genesis beacon state")
			return
		}
		if err := file.WriteFile(*yamlOutputFile, encodedState); err != nil {
			log.WithError(err).Print("Could not write encoded genesis beacon state to file")
			return
		}
		log.Printf("Done writing to %s", *yamlOutputFile)
	}
	if *jsonOutputFile != "" {
		encodedState, err := json.Marshal(genesisState)
		if err != nil {
			log.WithError(err).Print("Could not json marshal the genesis beacon state")
			return
		}
		if err := file.WriteFile(*jsonOutputFile, encodedState); err != nil {
			log.WithError(err).Print("Could not write encoded genesis beacon state to file")
			return
		}
		log.Printf("Done writing to %s", *jsonOutputFile)
	}
}

func genesisStateFromJSONValidators(r io.Reader, genesisTime uint64) (*ethpb.BeaconState, error) {
	enc, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var depositJSON []*DepositDataJSON
	if err := json.Unmarshal(enc, &depositJSON); err != nil {
		return nil, err
	}
	depositDataList := make([]*ethpb.Deposit_Data, len(depositJSON))
	depositDataRoots := make([][]byte, len(depositJSON))
	for i, val := range depositJSON {
		data, dataRootBytes, err := depositJSONToDepositData(val)
		if err != nil {
			return nil, err
		}
		depositDataList[i] = data
		depositDataRoots[i] = dataRootBytes
	}
	beaconState, _, err := interop.GenerateGenesisStateFromDepositData(context.Background(), genesisTime, depositDataList, depositDataRoots)
	if err != nil {
		return nil, err
	}
	return beaconState, nil
}

func depositJSONToDepositData(input *DepositDataJSON) (depositData *ethpb.Deposit_Data, dataRoot []byte, err error) {
	pubKeyBytes, err := hex.DecodeString(strings.TrimPrefix(input.PubKey, "0x"))
	if err != nil {
		return
	}
	withdrawalbytes, err := hex.DecodeString(strings.TrimPrefix(input.WithdrawalCredentials, "0x"))
	if err != nil {
		return
	}
	signatureBytes, err := hex.DecodeString(strings.TrimPrefix(input.Signature, "0x"))
	if err != nil {
		return
	}
	dataRootBytes, err := hex.DecodeString(strings.TrimPrefix(input.DepositDataRoot, "0x"))
	if err != nil {
		return
	}
	depositData = &ethpb.Deposit_Data{
		PublicKey:             pubKeyBytes,
		WithdrawalCredentials: withdrawalbytes,
		Amount:                input.Amount,
		Signature:             signatureBytes,
	}
	dataRoot = dataRootBytes
	return
}
