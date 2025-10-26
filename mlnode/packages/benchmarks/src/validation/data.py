from pydantic import (
    BaseModel,
    Field,
)
from typing import (
    List,
    Dict,
    Union
)
import pandas as pd


class PositionResult(BaseModel):
    token: str
    logprobs: Dict[str, float]


class Result(BaseModel):
    text: str
    results: List[PositionResult]


class ModelInfo(BaseModel):
    name: str
    url: str
    deploy_params: Dict[str, str] = Field(default_factory=dict)


class RequestParams(BaseModel):
    max_tokens: int
    temperature: float
    seed: int
    additional_params: Dict[str, Union[str, int, float]] = Field(default_factory=dict)
    top_logprobs: int = 3


class ValidationItem(BaseModel):
    prompt: str
    inference_result: Result
    validation_result: Result
    inference_model: ModelInfo
    validation_model: ModelInfo
    request_params: RequestParams

    def to_dict(self):
        return self.model_dump()

    def _count_single_choise(self) -> int:
        inf_single_choise = 0
        val_single_choise = 0
        not_valid_single_choise = 0
        not_valid_top_tokens = 0
        both_obvious = 0
        
        for inf_position, val_position in zip(
            self.inference_result.results, self.validation_result.results
        ):
            inf_logprobs = {k: v for k, v in inf_position.logprobs.items() if v > -9998}
            val_logprobs = {k: v for k, v in val_position.logprobs.items() if v > -9998}

            if len(inf_logprobs) == 1:
                inf_single_choise += 1
            if len(val_logprobs) == 1:
                val_single_choise += 1

            if len(inf_logprobs) == 1 and len(val_logprobs) != 1:
                not_valid_single_choise += 1

            if len(inf_logprobs) == 1 and len(val_logprobs) == 1:
                both_obvious += 1

            top_token_inf = max(inf_logprobs, key=inf_logprobs.get) if inf_logprobs else None
            top_token_val = max(val_logprobs, key=val_logprobs.get) if val_logprobs else None

            if top_token_inf != top_token_val:
                not_valid_top_tokens += 1

        return inf_single_choise, val_single_choise, not_valid_single_choise, not_valid_top_tokens, both_obvious
        



class ExperimentRequest(BaseModel):
    prompt: str
    inference_model: ModelInfo
    validation_model: ModelInfo
    request_params: RequestParams

    def to_result(self, inference_result: Result, validation_result: Result) -> ValidationItem:
        return ValidationItem(
            prompt=self.prompt,
            inference_result=inference_result,
            validation_result=validation_result,
            inference_model=self.inference_model,
            validation_model=self.validation_model,
            request_params=self.request_params
        )


def items_to_df(validation_results: List[ValidationItem]) -> pd.DataFrame:
    return pd.DataFrame([item.model_dump() for item in validation_results])


def df_to_items(df: pd.DataFrame) -> List[ValidationItem]:
    return [ValidationItem.model_validate(row) for row in df.to_dict(orient='records')]

def save_to_jsonl(
    validation_results: List[ValidationItem],
    path: str,
    append: bool = False
):
    mode = 'a' if append else 'w'
    with open(path, mode) as f:
        for result in validation_results:
            f.write(result.model_dump_json() + '\n')


def load_from_jsonl(
    path: str,
    n: int = None
) -> List[ValidationItem]:
    k = n if n is not None else float('inf')
    results = []
    with open(path, 'r') as f:
        for i, line in enumerate(f):
            if i >= k:
                break
            results.append(ValidationItem.model_validate_json(line))
    return results
