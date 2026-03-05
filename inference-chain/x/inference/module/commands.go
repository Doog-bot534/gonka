package inference

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference"
	"github.com/spf13/cobra"
)

func GrantMLOpsPermissionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant-ml-ops-permissions [account-address-or-key-name] <ml-operational-address>",
		Short: "Grant ML operations permissions from account key to ML operational key",
		Long: `Grant all ML operations permissions from account key to ML operational key.

This allows the ML operational key to perform automated ML operations on behalf of the account key.
The account key retains full control and can revoke these permissions at any time.

Arguments:
  account-address-or-key-name   (Optional) Bech32 address or keyring key name of the granter.
                                If omitted, uses --from flag value.
  ml-operational-address        Bech32 address of the ML operational key (hot wallet)

Examples:
  # Using key name (legacy)
  inferenced tx inference grant-ml-ops-permissions \
    gonka-account-key \
    gonka1rk52j24xj9ej87jas4zqpvjuhrgpnd7h3feqmm \
    --from gonka-account-key \
    --node http://node2.gonka.ai:8000/chain-rpc/

  # Using --from only (recommended, supports --generate-only for multisig)
  inferenced tx inference grant-ml-ops-permissions \
    gonka1rk52j24xj9ej87jas4zqpvjuhrgpnd7h3feqmm \
    --from gonka12c4ymjl6zzujvdgs26qlc59nvy0u4zk09fdt20 \
    --generate-only \
    --node http://node2.gonka.ai:8000/chain-rpc/

Note: Chain ID will be auto-detected from the chain if not specified with --chain-id`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			status, err := clientCtx.Client.Status(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to query chain status for chain-id: %w", err)
			}

			chainID := status.NodeInfo.Network
			cmd.Printf("Detected chain-id: %s\n", chainID)

			clientCtx = clientCtx.WithChainID(chainID)

			var operatorAddress sdk.AccAddress
			var mlOperationalAddressStr string

			if len(args) == 2 {
				mlOperationalAddressStr = args[1]
				operatorAddress, err = sdk.AccAddressFromBech32(args[0])
				if err != nil {
					keyInfo, keyErr := clientCtx.Keyring.Key(args[0])
					if keyErr != nil {
						return fmt.Errorf("invalid account address or key name %q: %w", args[0], err)
					}
					operatorAddress, err = keyInfo.GetAddress()
					if err != nil {
						return fmt.Errorf("failed to get address from key %q: %w", args[0], err)
					}
				}
			} else {
				mlOperationalAddressStr = args[0]
				operatorAddress = clientCtx.FromAddress
				if operatorAddress.Empty() {
					return fmt.Errorf("--from flag is required when account address is not provided as first argument")
				}
			}

			mlOperationalAddress, err := sdk.AccAddressFromBech32(mlOperationalAddressStr)
			if err != nil {
				return fmt.Errorf("invalid ML operational address: %w", err)
			}

			txFactory, err := tx.NewFactoryCLI(clientCtx, cmd.Flags())
			if err != nil {
				return err
			}

			txFactory = txFactory.WithChainID(clientCtx.ChainID)

			return inference.GrantMLOperationalKeyPermissions(
				clientCtx,
				txFactory,
				operatorAddress,
				mlOperationalAddress,
				nil, // Use default expiration (1 year)
			)
		},
	}

	flags.AddTxFlagsToCmd(cmd)
	return cmd
}
