package epochgroup_test

import (
	"sort"
	"strconv"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"go.uber.org/mock/gomock"
)

func getKeeperAndSdk(t *testing.T) (keeper.Keeper, sdk.Context) {
	k, ctx := keepertest.InferenceKeeper(t)
	return k, ctx
}

func FuzzCanFindUpperBound(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4}, 2)
	f.Fuzz(func(t *testing.T, data []byte, needle int) {
		haystack := make([]int, len(data))
		for i, b := range data {
			haystack[i] = int(b)
		}

		sort.Ints(haystack)

		i := epochgroup.UpperBound(needle, haystack)

		if i < 0 || i > len(haystack) {
			t.Fatalf("invalid index %d for len=%d", i, len(haystack))
		}

		for j := range i {
			if haystack[j] > needle {
				t.Fatalf("a[%d]=%d > x=%d", j, haystack[j], needle)
			}
		}
		if i < len(haystack) && haystack[i] <= needle {
			t.Fatalf("a[%d]=%d <= x=%d", i, haystack[i], needle)
		}
	})
}

func TestCanSample(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	pocStartHeight := int64(100)
	epochIndex := uint64(1)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: epochIndex, PocStartBlockHeight: pocStartHeight})
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, epochIndex))
	mocks.ExpectCreateGroupWithPolicyCall(ctx, epochIndex)

	eg, err := k.CreateEpochGroup(ctx, epochIndex, epochIndex)
	require.NoError(t, err)

	require.NoError(t, eg.CreateGroup(ctx))

	addr := testutil.Bech32Addr(42)
	participant := types.Participant {
		Index: addr,
		Address: addr,
		Weight: 450,
		Status: types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}
	require.NoError(t, eg.ParticipantKeeper.SetParticipant(ctx, participant))

	blockHash := []byte("blockhash")
	mocks.GroupKeeper.EXPECT().
		GroupMembers(ctx, gomock.Any()).
		Return(
			&group.QueryGroupMembersResponse {
				Members: []*group.GroupMember {
					&group.GroupMember {
						Member: &group.Member {
							Address: participant.Address,
							Weight: strconv.Itoa(int(participant.Weight)),
							Metadata: "",
							AddedAt: time.Now(),
						},
					},
				},
				Pagination: &query.PageResponse{},
			},
			nil,
		)
	rrCtx, err := eg.NewReplayableRandomContext(ctx, blockHash)
	require.NoError(t, err)

	expectedParticipant, err := eg.GetRandomMemberReplayable(ctx, rrCtx)
	require.NoError(t, err)
	require.Equal(t, *expectedParticipant, participant)

	expectedParticipant2, err := eg.GetRandomMemberReplayable(ctx, rrCtx)
	require.Error(t, err) // No participants to sample
	require.Nil(t, expectedParticipant2)
}
