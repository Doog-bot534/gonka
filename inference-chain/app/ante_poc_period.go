package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

type PocPeriodValidationDecorator struct {
	inferenceKeeper *inferencemodulekeeper.Keeper
}

func NewPocPeriodValidationDecorator(ik *inferencemodulekeeper.Keeper) PocPeriodValidationDecorator {
	return PocPeriodValidationDecorator{
		inferenceKeeper: ik,
	}
}

func (ppd PocPeriodValidationDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if simulate {
		return next(ctx, tx, simulate)
	}

	for _, msg := range tx.GetMsgs() {
		switch m := msg.(type) {
		case *inferencetypes.MsgSubmitPocBatch:
			if err := ppd.inferenceKeeper.ValidatePocPeriod(ctx, m.PocStageStartBlockHeight, inferencemodulekeeper.PocWindowBatch); err != nil {
				return ctx, err
			}
		case *inferencetypes.MsgSubmitPocValidation:
			if err := ppd.inferenceKeeper.ValidatePocPeriod(ctx, m.PocStageStartBlockHeight, inferencemodulekeeper.PocWindowValidation); err != nil {
				return ctx, err
			}
		}
	}

	return next(ctx, tx, simulate)
}
