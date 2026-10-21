import os
import secrets

from dotenv import load_dotenv

load_dotenv()

from fastapi import Depends, FastAPI, Header, HTTPException  # noqa: E402

from fetch import fetch_url_text  # noqa: E402
from judge import generate_rubric, grade_submission  # noqa: E402
from schemas import GradeRequest, GradeResponse, RubricRequest, RubricResponse  # noqa: E402

app = FastAPI(title="AI-Arbitrator AI Service")

INTERNAL_KEY = os.getenv("INTERNAL_API_KEY", "")


def require_internal_key(x_internal_key: str = Header(default="")) -> None:
    """Shared secret between the Go backend and this service. Without it, anyone could grade for free."""
    if not INTERNAL_KEY or not secrets.compare_digest(x_internal_key, INTERNAL_KEY):
        raise HTTPException(status_code=401, detail="Unauthorized")


@app.get("/health")
async def health() -> dict:
    return {"ok": True}


@app.post("/rubric", response_model=RubricResponse, dependencies=[Depends(require_internal_key)])
async def rubric(req: RubricRequest) -> RubricResponse:
    try:
        criteria = await generate_rubric(req.title, req.description)
    except Exception as e:  # noqa: BLE001
        raise HTTPException(status_code=502, detail=f"Rubric generation failed: {e}") from e
    return RubricResponse(criteria=criteria)


@app.post("/grade", response_model=GradeResponse, dependencies=[Depends(require_internal_key)])
async def grade(req: GradeRequest) -> GradeResponse:
    submission = req.submission
    if req.submission_type == "link":
        try:
            submission = await fetch_url_text(req.submission)
        except ValueError as e:
            raise HTTPException(status_code=422, detail=str(e)) from e
        except Exception as e:  # noqa: BLE001
            raise HTTPException(status_code=422, detail=f"Could not fetch link: {e}") from e

    try:
        return await grade_submission(req.description, req.criteria, submission, req.submission_type)
    except Exception as e:  # noqa: BLE001
        raise HTTPException(status_code=502, detail=f"Grading failed: {e}") from e
