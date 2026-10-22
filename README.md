# AI-Arbitrator

AI-Arbitrator is a prototype escrow workflow for client-freelancer work. A client defines acceptance requirements, the freelancer submits work, and an AI service grades the submission against an agreed rubric. In the deployed flow, an oracle relayer reports the result to an escrow contract.

## Project status

- Solidity escrow contract and Foundry tests are in `contracts/`.
- The Go API, PostgreSQL persistence, chain listener, and grading worker are in `backend/`.
- The FastAPI rubric and grading service is in `ai-service/`.
- The React frontend is in `frontend/` and supports the create, rubric confirmation, dev funding, submission, and verdict flow.
- The current frontend is a local dev-mode demo UI. Wallet connection and real on-chain transaction controls are not implemented yet.
- The backend currently supports `DEV_MODE=true` for exercising the workflow without a deployed contract. Dev funding is simulated and does not send a blockchain transaction.
- A Sepolia deployment and real-wallet flow are not included in the current setup.

## Architecture

```text
Client / Freelancer UI (planned)
        |
        v
Go API + PostgreSQL <----> FastAPI grading service
        |
        v
Escrow contract (Sepolia when configured)
```

The backend proposes a rubric, stores the client's confirmed requirements, and calculates a requirements hash. A freelancer's submission is hashed before it is graded. In chain-enabled mode, the frontend submits that hash to the contract; the backend listener and worker grade the confirmed submission and the oracle relayer settles the escrow.

## Local development

Copy the example environment files and fill in local values. Never commit `.env` files or real API keys, wallet keys, or RPC credentials.

Start PostgreSQL once from the testing terminal:

**[Terminal 3 — Git Bash, repository root]**

```bash
docker compose up -d
```

**[Terminal 2 — Git Bash, AI service]**

```bash
cd ~/ai-arbitrator/ai-service
source .venv/Scripts/activate
uvicorn main:app --port 8000
```

**[Terminal 1 — Git Bash, Go backend]**

```bash
cd ~/ai-arbitrator/backend
go run ./cmd/server
```

Check the health endpoints from **[Terminal 3 — Git Bash, testing]**:

```bash
curl -sS http://localhost:8080/health
curl -sS http://localhost:8000/health
```

The backend reads `backend/.env`; the AI service reads `ai-service/.env`. Set `INTERNAL_API_KEY` to the same local value in both files. The provided Docker Compose file exposes PostgreSQL on `127.0.0.1:5433`.

Run the frontend in **[Terminal 3 — Git Bash, frontend]** after PostgreSQL has started:

```bash
cd ~/ai-arbitrator/frontend
pnpm install
pnpm dev
```

Open `http://localhost:5173`. Vite proxies `/api` and `/health` requests to the Go backend on port 8080.

Health endpoints:

- Backend: `http://localhost:8080/health`
- AI service: `http://localhost:8000/health`

The backend API is under `/api`. In dev mode, it also exposes `POST /api/dev/jobs/:id/fund` to simulate funding. This mode is for local demos only.

## Security notes

- Use throwaway testnet wallets and testnet-only funds for demos.
- Keep `.env` files, API keys, private keys, and RPC credentials out of Git.
- Do not treat AI grading as a substitute for human review in production. The current project is a hackathon prototype.

## License

No license has been selected. Unless a license is added, others do not have permission to reuse this code by default.
