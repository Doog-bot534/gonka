import math
from typing import List, Dict
from dataclasses import dataclass


@dataclass
class AnalyticalResult:
    num_participants: int
    num_trees: int
    fanout: int
    
    tree_depth: int
    avg_path_length: float
    
    min_nodes_to_block_one: int
    max_nodes_to_block_one: int
    
    expected_blocked_one_node: float
    expected_blocked_ten_percent: float
    expected_blocked_twenty_percent: float
    
    lower_bound_45_percent: int
    lower_bound_50_percent: int
    
    tree_redundancy_factor: float


def factorial(n: int) -> int:
    if n <= 1:
        return 1
    result = 1
    for i in range(2, n + 1):
        result *= i
    return result


def comb(n: int, k: int) -> float:
    if k > n or k < 0:
        return 0
    if k == 0 or k == n:
        return 1
    k = min(k, n - k)
    result = 1
    for i in range(k):
        result = result * (n - i) / (i + 1)
    return result


def calculate_path_length_distribution(num_participants: int, fanout: int) -> Dict[int, int]:
    distribution = {}
    for i in range(num_participants):
        if i == 0:
            path_length = 0
        else:
            depth = 0
            pos = i
            while pos > 0:
                pos = (pos - 1) // fanout
                depth += 1
            path_length = depth
        
        if path_length not in distribution:
            distribution[path_length] = 0
        distribution[path_length] += 1
    
    return distribution


def analytical_analysis(num_participants: int, num_trees: int, fanout: int) -> AnalyticalResult:
    tree_depth = math.ceil(math.log(num_participants, fanout)) if fanout > 1 else num_participants - 1
    
    path_dist = calculate_path_length_distribution(num_participants, fanout)
    avg_path_length = sum(length * count for length, count in path_dist.items()) / num_participants
    
    min_nodes_to_block_one = min(num_trees * 1, num_participants - 1)
    max_nodes_to_block_one = min(num_trees * tree_depth, num_participants - 1)
    
    def prob_path_blocked(path_length: int, num_controlled: int) -> float:
        if path_length == 0:
            return 0.0
        if num_controlled >= num_participants - 1:
            return 1.0
        if num_controlled == 0:
            return 0.0
        
        prob_no_node_controlled = 1.0
        for i in range(path_length):
            prob_no_node_controlled *= (num_participants - 1 - num_controlled - i) / (num_participants - 1 - i)
        
        return 1.0 - prob_no_node_controlled
    
    def expected_blocked_fraction(num_controlled: int) -> float:
        if num_controlled >= num_participants:
            return 1.0
        
        total_prob = 0.0
        for path_length, count in path_dist.items():
            if path_length == 0:
                continue
            
            prob_single_path_blocked = prob_path_blocked(path_length, num_controlled)
            prob_all_trees_blocked = prob_single_path_blocked ** num_trees
            
            total_prob += prob_all_trees_blocked * count
        
        return total_prob / (num_participants - 1) if num_participants > 1 else 0.0
    
    expected_blocked_one_node = expected_blocked_fraction(1)
    expected_blocked_ten_percent = expected_blocked_fraction(max(1, num_participants // 10))
    expected_blocked_twenty_percent = expected_blocked_fraction(max(1, num_participants // 5))
    
    def find_min_to_block_fraction(target_fraction: float) -> int:
        for num_controlled in range(1, num_participants):
            if expected_blocked_fraction(num_controlled) >= target_fraction:
                return num_controlled
        return num_participants
    
    lower_bound_45 = find_min_to_block_fraction(0.45)
    lower_bound_50 = find_min_to_block_fraction(0.50)
    
    tree_redundancy = 1.0 - (1.0 / num_trees)
    
    return AnalyticalResult(
        num_participants=num_participants,
        num_trees=num_trees,
        fanout=fanout,
        tree_depth=tree_depth,
        avg_path_length=avg_path_length,
        min_nodes_to_block_one=min_nodes_to_block_one,
        max_nodes_to_block_one=max_nodes_to_block_one,
        expected_blocked_one_node=expected_blocked_one_node,
        expected_blocked_ten_percent=expected_blocked_ten_percent,
        expected_blocked_twenty_percent=expected_blocked_twenty_percent,
        lower_bound_45_percent=lower_bound_45,
        lower_bound_50_percent=lower_bound_50,
        tree_redundancy_factor=tree_redundancy
    )


def print_analysis(result: AnalyticalResult):
    print(f"\n{'='*80}")
    print(f"Configuration: {result.num_participants} participants, {result.num_trees} trees, fanout {result.fanout}")
    print(f"{'='*80}")
    
    print(f"\nTree Structure:")
    print(f"  Depth: {result.tree_depth}")
    print(f"  Avg path length: {result.avg_path_length:.2f}")
    print(f"  Redundancy factor: {result.tree_redundancy_factor:.2%}")
    
    print(f"\nSingle Participant Blocking:")
    print(f"  Min nodes required: {result.min_nodes_to_block_one}")
    print(f"  Max nodes required: {result.max_nodes_to_block_one}")
    
    print(f"\nRandom Adversary (Expected % Blocked):")
    print(f"  1 node controlled:              {result.expected_blocked_one_node:.4%}")
    print(f"  10% nodes controlled ({result.num_participants//10:<4}):     {result.expected_blocked_ten_percent:.4%}")
    print(f"  20% nodes controlled ({result.num_participants//5:<4}):     {result.expected_blocked_twenty_percent:.4%}")
    
    print(f"\nTargeted Attack (Lower Bounds):")
    print(f"  To block ≥45%: {result.lower_bound_45_percent:>5} nodes ({result.lower_bound_45_percent/result.num_participants*100:>5.2f}%)")
    print(f"  To block ≥50%: {result.lower_bound_50_percent:>5} nodes ({result.lower_bound_50_percent/result.num_participants*100:>5.2f}%)")
    
    print(f"\nSecurity Metrics:")
    attack_resistance = result.lower_bound_50_percent / result.num_participants
    if attack_resistance > 0.4:
        rating = "EXCELLENT"
    elif attack_resistance > 0.3:
        rating = "GOOD"
    elif attack_resistance > 0.2:
        rating = "FAIR"
    else:
        rating = "WEAK"
    print(f"  Attack resistance: {rating} ({attack_resistance:.2%} nodes needed for 50% block)")


def main():
    print("Mathematical Analysis of Propagation Trees")
    print("="*80)
    
    participant_counts = [100, 500, 1000, 5000, 10000]
    tree_counts = [4, 6, 8, 10, 12]
    fanouts = [4, 8, 16, 32]
    
    for num_participants in participant_counts:
        for num_trees in tree_counts:
            for fanout in fanouts:
                result = analytical_analysis(num_participants, num_trees, fanout)
                print_analysis(result)
    
    print(f"\n{'='*80}")
    print("Analysis complete")
    print(f"{'='*80}")
    
    print("\n\nKey Formulas Used:")
    print("="*80)
    print("1. Tree Depth: d = ⌈log_f(n)⌉")
    print("2. P(path blocked | m controlled): 1 - ∏(n-1-m-i)/(n-1-i) for i in [0, L)")
    print("3. P(participant blocked): ∏ P(path_i blocked) for all k trees")
    print("4. E[% blocked]: (1/n) Σ P(participant_i blocked)")
    print("5. Min to block 50%: min{m : E[% blocked | m] ≥ 0.5}")
    print("="*80)


if __name__ == "__main__":
    main()
