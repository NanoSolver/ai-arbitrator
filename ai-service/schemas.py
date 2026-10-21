from typing import Literal

from pydantic import BaseModel, Field


class RubricRequest(BaseModel):
    title: str = Field(min_length=1, max_length=200)
    description: str = Field(min_length=10, max_length=5000)


class Criterion(BaseModel):
    id: str = Field(max_length=32)
    text: str = Field(max_length=500)
    required: bool = True


class RubricResponse(BaseModel):
    criteria: list[Criterion]


class GradeRequest(BaseModel):
    description: str = Field(min_length=1, max_length=5000)
    criteria: list[Criterion] = Field(min_length=1, max_length=10)
    # For submission_type == "link" this is the URL; the service fetches the content itself.
    submission: str = Field(min_length=1, max_length=20000)
    submission_type: Literal["text", "code", "link"] = "text"


class CriterionResult(BaseModel):
    id: str
    passed: bool
    reason: str


class GradeResponse(BaseModel):
    verdict: Literal["PASS", "FAIL"]
    criteria_results: list[CriterionResult]
    reasoning: str
    feedback: str
    injection_detected: bool
