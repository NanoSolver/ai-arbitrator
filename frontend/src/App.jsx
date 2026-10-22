import { useCallback, useEffect, useMemo, useState } from "react";

const API = import.meta.env.VITE_API_BASE_URL || "";
const DEMO_FREELANCER = "0x70997970C51812dc3A010C7d01b50e0d17dc79C8";
const stages = ["DRAFT", "AWAITING_FUNDING", "FUNDED", "SUBMITTED", "RELEASED"];
const statusNames = {
  DRAFT: "Rubric review",
  AWAITING_FUNDING: "Awaiting funding",
  FUNDED: "In progress",
  SUBMITTED: "Awaiting verdict",
  REVISION_REQUESTED: "Revision requested",
  RELEASED: "Released",
  REFUNDED: "Refunded",
};

async function request(path, options = {}) {
  const response = await fetch(`${API}${path}`, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const text = await response.text();
  let body;
  try { body = text ? JSON.parse(text) : null; } catch { body = text; }
  if (!response.ok) {
    const message = body?.error || body?.detail || `Request failed (${response.status})`;
    throw new Error(message);
  }
  return body;
}

function shortId(id = "") {
  return id.length > 12 ? `${id.slice(0, 6)}…${id.slice(-4)}` : id;
}

function relativeDate(value) {
  if (!value) return "Just now";
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 1000));
  if (seconds < 60) return "Just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

function statusClass(status = "") {
  if (["RELEASED", "DONE"].includes(status)) return "success";
  if (["REVISION_REQUESTED", "REFUNDED", "FAILED"].includes(status)) return "warning";
  if (["SUBMITTED", "GRADING", "QUEUED", "SETTLING"].includes(status)) return "review";
  return "neutral";
}

function App() {
  const [config, setConfig] = useState(null);
  const [health, setHealth] = useState("checking");
  const [jobs, setJobs] = useState([]);
  const [selectedId, setSelectedId] = useState(() => localStorage.getItem("arbiter:selectedJob") || "");
  const [selectedJob, setSelectedJob] = useState(null);
  const [verdict, setVerdict] = useState(null);
  const [view, setView] = useState("workspace");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [form, setForm] = useState({ title: "", description: "", freelancer: DEMO_FREELANCER });
  const [criteria, setCriteria] = useState([]);
  const [submission, setSubmission] = useState({ type: "code", content: "" });

  const loadJobs = useCallback(async () => {
    const data = await request("/api/jobs");
    setJobs(Array.isArray(data) ? data : []);
  }, []);

  const loadSelected = useCallback(async (id) => {
    if (!id) { setSelectedJob(null); setVerdict(null); return; }
    const [job, result] = await Promise.all([
      request(`/api/jobs/${id}`),
      request(`/api/jobs/${id}/verdict`),
    ]);
    setSelectedJob(job);
    setVerdict(result?.submission || null);
    setCriteria(job.criteria || []);
  }, []);

  const refresh = useCallback(async () => {
    try {
      const [, backendConfig] = await Promise.all([request("/health"), request("/api/config")]);
      setHealth("online");
      setConfig(backendConfig);
      await loadJobs();
      if (selectedId) await loadSelected(selectedId);
      setError("");
    } catch (err) {
      setHealth("offline");
      setError(err.message);
    }
  }, [loadJobs, loadSelected, selectedId]);

  useEffect(() => { refresh(); }, [refresh]);

  useEffect(() => {
    if (!selectedId || !selectedJob || !["SUBMITTED"].includes(selectedJob.status)) return undefined;
    const timer = window.setInterval(() => {
      loadSelected(selectedId).then(loadJobs).catch((err) => setError(err.message));
    }, 2500);
    return () => window.clearInterval(timer);
  }, [selectedId, selectedJob?.status, loadSelected, loadJobs]);

  useEffect(() => {
    if (!notice) return undefined;
    const timer = window.setTimeout(() => setNotice(""), 3600);
    return () => window.clearTimeout(timer);
  }, [notice]);

  function selectJob(id) {
    setSelectedId(id);
    localStorage.setItem("arbiter:selectedJob", id);
    setView("workspace");
    setError("");
    loadSelected(id).catch((err) => setError(err.message));
  }

  async function createJob(event) {
    event.preventDefault();
    setBusy(true); setError(""); setNotice("");
    try {
      const job = await request("/api/jobs", { method: "POST", body: JSON.stringify(form) });
      setSelectedId(job.id);
      localStorage.setItem("arbiter:selectedJob", job.id);
      setSelectedJob(job);
      setCriteria(job.criteria || []);
      setVerdict(null);
      setView("workspace");
      setNotice("Rubric generated. Review the criteria before confirming.");
      setForm((current) => ({ ...current, title: "", description: "" }));
      await loadJobs();
    } catch (err) { setError(err.message); }
    finally { setBusy(false); }
  }

  async function confirmRubric() {
    setBusy(true); setError("");
    try {
      const job = await request(`/api/jobs/${selectedId}/confirm`, {
        method: "POST",
        body: JSON.stringify({ criteria: criteria.map(({ text, required }) => ({ text, required })) }),
      });
      setSelectedJob(job);
      setCriteria(job.criteria || criteria);
      setNotice("Requirements locked. The job is ready for funding.");
      await loadJobs();
    } catch (err) { setError(err.message); }
    finally { setBusy(false); }
  }

  async function fundInDevMode() {
    setBusy(true); setError("");
    try {
      const job = await request(`/api/dev/jobs/${selectedId}/fund`, { method: "POST", body: JSON.stringify({}) });
      setSelectedJob(job);
      setNotice("Demo funding simulated. No blockchain transaction was sent.");
      await loadJobs();
    } catch (err) { setError(err.message); }
    finally { setBusy(false); }
  }

  async function submitWork(event) {
    event.preventDefault();
    setBusy(true); setError("");
    try {
      await request(`/api/jobs/${selectedId}/submissions`, {
        method: "POST",
        body: JSON.stringify(submission),
      });
      await loadSelected(selectedId);
      await loadJobs();
      setNotice("Work submitted. AI arbitration is in progress.");
      setSubmission((current) => ({ ...current, content: "" }));
    } catch (err) { setError(err.message); }
    finally { setBusy(false); }
  }

  const sortedJobs = useMemo(() => [...jobs].sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt)), [jobs]);
  const openCount = jobs.filter((job) => !["RELEASED", "REFUNDED"].includes(job.status)).length;
  const reviewCount = jobs.filter((job) => job.status === "SUBMITTED").length;

  function updateCriterion(index, key, value) {
    setCriteria((current) => current.map((criterion, i) => i === index ? { ...criterion, [key]: value } : criterion));
  }

  function addCriterion() {
    setCriteria((current) => [...current, { id: `c${current.length + 1}`, text: "", required: true }]);
  }

  function removeCriterion(index) {
    setCriteria((current) => current.filter((_, i) => i !== index));
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <a className="brand" href="#workspace" onClick={() => setView("workspace")}>
          <span className="brand-mark"><span /></span>
          <span className="brand-name">arbiter<span className="brand-period">.</span></span>
        </a>
        <div className="side-label">WORKSPACE</div>
        <button className={`nav-item ${view === "workspace" ? "active" : ""}`} onClick={() => setView("workspace")}>
          <Icon name="grid" /> <span>Overview</span><span className="nav-shortcut">⌘ 1</span>
        </button>
        <button className={`nav-item ${view === "jobs" ? "active" : ""}`} onClick={() => setView("jobs")}>
          <Icon name="layers" /> <span>All agreements</span><span className="nav-count">{jobs.length}</span>
        </button>
        <div className="side-label side-label-spaced">TOOLS</div>
        <div className="tool-link"><Icon name="shield" /><span>AI arbitration</span><span className="tool-live" /></div>
        <div className="sidebar-spacer" />
        <div className="sidebar-note">
          <div className="note-icon"><Icon name="spark" /></div>
          <strong>Built for fair handoffs</strong>
          <span>Clear terms in. Accountable decisions out.</span>
        </div>
        <div className="profile-row">
          <div className="avatar">P</div>
          <div className="profile-copy"><strong>Parikshit</strong><span>Builder account</span></div>
          <button className="icon-button profile-menu" aria-label="Account menu">···</button>
        </div>
      </aside>

      <main className="main-area">
        <header className="topbar">
          <div className="breadcrumbs"><span>Workspace</span><i>/</i><strong>{view === "jobs" ? "All agreements" : "Overview"}</strong></div>
          <div className="topbar-right">
            <div className={`connection ${health}`}><span className="connection-dot" />{health === "online" ? "API connected" : health === "checking" ? "Connecting" : "API offline"}</div>
            <div className="dev-badge"><span className="dev-glyph">⌘</span> DEV MODE</div>
            <div className="top-avatar">P</div>
          </div>
        </header>

        <div className="page-content">
          <section className="welcome-row">
            <div>
              <div className="eyebrow"><span className="eyebrow-line" /> ESCROW WORKSPACE <span className="eyebrow-date">{new Intl.DateTimeFormat("en", { weekday: "long", month: "short", day: "numeric" }).format(new Date())}</span></div>
              <h1>{view === "jobs" ? "Your agreements" : "Make work feel fair."}</h1>
              <p className="welcome-subtitle">Set clear terms, submit the work, and let every decision show its reasoning.</p>
            </div>
            <button className="primary-button new-job-button" onClick={() => { setView("workspace"); document.getElementById("new-job-title")?.focus(); }}>
              <span className="plus-sign">+</span> New agreement
            </button>
          </section>

          {error && <div className="alert alert-error"><Icon name="alert" /><span>{error}</span><button onClick={() => setError("")} aria-label="Dismiss">×</button></div>}
          {notice && <div className="alert alert-success"><Icon name="check" /><span>{notice}</span></div>}

          <section className="stats-grid">
            <StatCard label="Active agreements" value={openCount.toString().padStart(2, "0")} detail="Across your workspace" icon="layers" tone="mint" />
            <StatCard label="Awaiting decision" value={reviewCount.toString().padStart(2, "0")} detail="AI review in progress" icon="spark" tone="blue" />
            <StatCard label="Settled" value={jobs.filter((job) => ["RELEASED", "REFUNDED"].includes(job.status)).length.toString().padStart(2, "0")} detail="Completed agreements" icon="check" tone="peach" />
            <div className="stat-card network-card">
              <div className="stat-topline"><span>NETWORK</span><span className="network-pulse" /></div>
              <div className="network-title">{config?.devMode ? "Local sandbox" : config?.chainEnabled ? "Sepolia testnet" : "Not configured"}</div>
              <div className="network-caption">{config?.devMode ? "Transactions are simulated" : config?.chainEnabled ? "Oracle connected" : "Configure chain to go live"}</div>
            </div>
          </section>

          <div className="workspace-grid">
            <section className="panel agreements-panel">
              <div className="panel-heading">
                <div><span className="section-kicker">YOUR WORKSPACE</span><h2>Recent agreements</h2></div>
                <button className="text-button" onClick={() => setView("jobs")}>View all <span>↗</span></button>
              </div>
              <div className="agreement-list">
                {sortedJobs.length === 0 ? (
                  <div className="empty-jobs">
                    <div className="empty-symbol"><Icon name="layers" /></div>
                    <strong>No agreements yet</strong>
                    <span>Create your first agreement to start a fair, transparent handoff.</span>
                  </div>
                ) : (view === "jobs" ? sortedJobs : sortedJobs.slice(0, 5)).map((job) => (
                  <AgreementRow key={job.id} job={job} selected={job.id === selectedId} onClick={() => selectJob(job.id)} />
                ))}
              </div>
              {sortedJobs.length > 0 && <div className="list-foot"><span>Showing {view === "jobs" ? sortedJobs.length : Math.min(sortedJobs.length, 5)} of {sortedJobs.length} agreements</span><button className="icon-button" onClick={refresh} aria-label="Refresh list">↻</button></div>}
            </section>

            <section className="panel detail-panel">
              {selectedJob ? (
                <JobDetail
                  job={selectedJob}
                  criteria={criteria}
                  setCriteria={setCriteria}
                  updateCriterion={updateCriterion}
                  addCriterion={addCriterion}
                  removeCriterion={removeCriterion}
                  verdict={verdict}
                  submission={submission}
                  setSubmission={setSubmission}
                  busy={busy}
                  devMode={Boolean(config?.devMode)}
                  onConfirm={confirmRubric}
                  onFund={fundInDevMode}
                  onSubmit={submitWork}
                />
              ) : (
                <CreateJob
                  form={form}
                  setForm={setForm}
                  busy={busy}
                  onSubmit={createJob}
                />
              )}
            </section>
          </div>

          <footer className="page-footer"><span><span className="footer-mark">A</span> AI-Arbitrator <span className="footer-dot">·</span> Transparent by design</span><span>Prototype <b>0.1</b></span></footer>
        </div>
      </main>
    </div>
  );
}

function StatCard({ label, value, detail, icon, tone }) {
  return <div className="stat-card"><div className="stat-topline"><span>{label}</span><span className={`stat-icon ${tone}`}><Icon name={icon} /></span></div><div className="stat-value">{value}</div><div className="stat-detail">{detail}</div></div>;
}

function AgreementRow({ job, selected, onClick }) {
  const letter = (job.title || "A").trim().charAt(0).toUpperCase();
  return (
    <button className={`agreement-row ${selected ? "selected" : ""}`} onClick={onClick}>
      <span className={`job-avatar avatar-${letter.charCodeAt(0) % 5}`}>{letter}</span>
      <span className="job-info"><strong>{job.title || "Untitled agreement"}</strong><span>#{shortId(job.id)} <i>·</i> {relativeDate(job.createdAt)}</span></span>
      <span className={`status-pill ${statusClass(job.status)}`}><span />{statusNames[job.status] || job.status}</span>
      <span className="row-arrow">↗</span>
    </button>
  );
}

function CreateJob({ form, setForm, busy, onSubmit }) {
  return (
    <>
      <div className="panel-heading detail-heading"><div><span className="section-kicker">GET STARTED</span><h2>Create an agreement</h2></div><div className="step-chip"><span>01</span> OF 04</div></div>
      <p className="detail-intro">Tell us what needs to be delivered. AI will turn your brief into a clear acceptance rubric.</p>
      <form className="create-form" onSubmit={onSubmit}>
        <label className="field-label" htmlFor="new-job-title">PROJECT NAME</label>
        <input id="new-job-title" className="text-input" maxLength={200} placeholder="e.g. Landing page redesign" value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} required />
        <div className="field-label-row"><label className="field-label" htmlFor="job-description">WHAT DOES DONE LOOK LIKE?</label><span className="field-hint">Be specific</span></div>
        <textarea id="job-description" className="text-input text-area" maxLength={5000} minLength={10} placeholder="Describe the deliverables and how you will decide they are complete…" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} required />
        <div className="field-label-row"><label className="field-label" htmlFor="freelancer-address">FREELANCER WALLET</label><span className="field-hint">Sepolia address</span></div>
        <div className="address-input-wrap"><span className="address-prefix">◇</span><input id="freelancer-address" className="text-input address-input" value={form.freelancer} onChange={(e) => setForm({ ...form, freelancer: e.target.value })} required /></div>
        <div className="form-foot"><span className="secure-note"><Icon name="lock" /> Requirements stay editable until you confirm</span><button className="primary-button" type="submit" disabled={busy || !form.title.trim() || form.description.trim().length < 10}>{busy ? <><span className="spinner" /> Creating…</> : <>Generate rubric <span>→</span></>}</button></div>
      </form>
      <div className="privacy-note"><span className="privacy-icon"><Icon name="spark" /></span><span><strong>Fair starts with clarity.</strong> Your rubric is the shared source of truth for every review.</span></div>
    </>
  );
}

function JobDetail({ job, criteria, setCriteria, updateCriterion, addCriterion, removeCriterion, verdict, submission, setSubmission, busy, devMode, onConfirm, onFund, onSubmit }) {
  const isDraft = job.status === "DRAFT";
  const canFund = job.status === "AWAITING_FUNDING" && devMode;
  const canSubmit = ["FUNDED", "REVISION_REQUESTED"].includes(job.status);
  const currentStage = job.status === "REVISION_REQUESTED" ? 2 : job.status === "REFUNDED" ? 4 : Math.max(0, stages.indexOf(job.status));
  const latestSubmission = verdict;
  const reviewFailed = latestSubmission?.status === "FAILED";
  return (
    <>
      <div className="panel-heading detail-heading">
        <div><span className="section-kicker">AGREEMENT DETAILS</span><h2 className="detail-title">{job.title}</h2></div>
        <button className={`status-pill detail-status ${statusClass(reviewFailed ? "FAILED" : job.status)}`}><span />{reviewFailed ? "Review failed" : statusNames[job.status] || job.status}</button>
      </div>
      <div className="detail-meta"><span className="meta-id">#{shortId(job.id)}</span><span>Created {relativeDate(job.createdAt)}</span><span className="meta-sep">·</span><span>{(job.criteria || []).length} criteria</span></div>
      <div className="progress-track" aria-label="Agreement progress">
        {stages.map((stage, i) => <div className={`progress-step ${i <= currentStage ? "done" : ""} ${i === currentStage ? "current" : ""}`} key={stage}><span className="step-dot">{i < currentStage ? "✓" : `0${i + 1}`}</span><span className="progress-label">{["Brief", "Confirm", "Fund", "Review", "Settle"][i]}</span>{i < stages.length - 1 && <span className="progress-line" />}</div>)}
      </div>

      <div className="detail-body">
        <div className="description-block"><span className="field-label">PROJECT BRIEF</span><p>{job.description}</p></div>
        <div className="criteria-heading"><div><span className="field-label">ACCEPTANCE RUBRIC</span><span className="rubric-subtitle">{isDraft ? "Review and adjust before locking the requirements." : "These requirements were agreed before work began."}</span></div><span className="criteria-count">{criteria.length} ITEMS</span></div>
        <div className="criteria-list">
          {criteria.map((item, index) => <div className="criterion" key={`${item.id}-${index}`}>
            <span className={`criterion-index ${item.required ? "required" : "optional"}`}>{String(index + 1).padStart(2, "0")}</span>
            {isDraft ? <input className="criterion-input" value={item.text} onChange={(e) => updateCriterion(index, "text", e.target.value)} aria-label={`Criterion ${index + 1}`} /> : <span className="criterion-text">{item.text}</span>}
            {isDraft ? <><button className={`required-toggle ${item.required ? "on" : ""}`} onClick={() => updateCriterion(index, "required", !item.required)} type="button">{item.required ? "Required" : "Optional"}</button><button className="remove-criterion" onClick={() => removeCriterion(index)} aria-label="Remove criterion">×</button></> : <span className={`criterion-tag ${item.required ? "" : "optional-tag"}`}>{item.required ? "Required" : "Optional"}</span>}
          </div>)}
          {criteria.length === 0 && <div className="no-criteria">No criteria yet. Add at least one required item to continue.</div>}
        </div>
        {isDraft && <button className="add-criterion" onClick={addCriterion} type="button"><span>+</span> Add criterion</button>}

        {canFund && <div className="next-step-card"><div className="next-step-icon"><Icon name="lock" /></div><div><strong>Lock the agreed terms</strong><span>Confirm this rubric, then simulate funding to test the workflow.</span></div><button className="primary-button" onClick={onFund} disabled={busy}>Simulate funding <span>→</span></button></div>}

        {canSubmit && <form className="submission-form" onSubmit={onSubmit}>
          <div className="submission-title-row"><div><span className="field-label">SUBMIT YOUR WORK</span><span className="rubric-subtitle">Your submission is checked against the agreed rubric.</span></div><span className="attempt-chip">ATTEMPT {Number(job.revisions || 0) + 1}</span></div>
          <div className="submission-type-row"><label htmlFor="submission-type">FORMAT</label><select id="submission-type" value={submission.type} onChange={(e) => setSubmission({ ...submission, type: e.target.value })}><option value="code">Code</option><option value="text">Text</option><option value="link">Link</option></select></div>
          <textarea className="text-input submission-input" maxLength={20000} placeholder={submission.type === "link" ? "https://…" : "Paste the deliverable or code here…"} value={submission.content} onChange={(e) => setSubmission({ ...submission, content: e.target.value })} required />
          <div className="submit-foot"><span>{submission.content.length.toLocaleString()} / 20,000</span><button className="primary-button" disabled={busy || !submission.content.trim()}>{busy ? <><span className="spinner" /> Submitting…</> : <>Submit for review <span>→</span></>}</button></div>
        </form>}

        {job.status === "SUBMITTED" && !["DONE", "FAILED"].includes(latestSubmission?.status) && <div className="review-state"><span className="review-spinner"><span /></span><div><strong>{latestSubmission?.status === "GRADING" ? "Evaluating submission" : latestSubmission?.status === "QUEUED" ? "Submission queued" : "Review in progress"}</strong><span>Checking each criterion and preparing a transparent verdict…</span></div><span className="live-label"><i /> LIVE</span></div>}

        {latestSubmission && ["DONE", "FAILED"].includes(latestSubmission.status) && <VerdictCard verdict={latestSubmission} job={job} />}

        {job.requirementsHash && <div className="hash-row"><span><Icon name="fingerprint" /> REQUIREMENTS HASH</span><code title={job.requirementsHash}>{shortId(job.requirementsHash)}</code></div>}
        {!isDraft && job.freelancer && <div className="people-row"><span className="field-label">FREELANCER</span><code title={job.freelancer}>{shortId(job.freelancer)}</code></div>}
      </div>

      {isDraft && <div className="detail-footer"><span className="secure-note"><Icon name="lock" /> Confirmation locks the rubric hash</span><button className="primary-button" onClick={onConfirm} disabled={busy || criteria.length === 0 || criteria.some((item) => !item.text.trim()) || !criteria.some((item) => item.required)}>{busy ? <><span className="spinner" /> Confirming…</> : <>Confirm requirements <span>→</span></>}</button></div>}
    </>
  );
}

function VerdictCard({ verdict, job }) {
  const pass = verdict.verdict === "PASS";
  const failed = verdict.status === "FAILED";
  return (
    <div className={`verdict-card ${pass ? "pass" : "fail"}`}>
      <div className="verdict-top"><span className={`verdict-symbol ${pass ? "" : "fail-symbol"}`}>{pass ? "✓" : "!"}</span><div><span className="field-label">{failed ? "AI REVIEW · ATTEMPT" : "AI VERDICT · ATTEMPT"} {verdict.attempt || (job.revisions + 1)}</span><strong>{failed ? "Grading failed" : pass ? "Requirements met" : "Needs another pass"}</strong></div><span className={`verdict-label ${pass ? "" : "fail-label"}`}>{failed ? "ERROR" : verdict.verdict}</span></div>
      {verdict.injectionDetected && <div className="injection-alert"><Icon name="alert" /> Prompt injection detected and disregarded.</div>}
      <div className="result-list">{(verdict.criteriaResults || []).map((result) => <div className="result-row" key={result.id}><span className={`result-check ${result.passed ? "yes" : "no"}`}>{result.passed ? "✓" : "×"}</span><span className="result-reason">{result.reason}</span></div>)}</div>
      {verdict.feedback && <div className="verdict-feedback"><span className="field-label">FEEDBACK</span><p>{verdict.feedback}</p></div>}
      {verdict.error && <div className="verdict-feedback"><span className="field-label">REVIEW ERROR</span><p>{verdict.error}</p></div>}
      <div className="verdict-footer"><span>{failed ? "No verdict recorded — check the grading service" : verdict.action === "RELEASE" ? "Escrow released" : verdict.action === "REJECT" ? "Revision requested" : verdict.action === "REFUND" ? "Client refunded" : "Outcome recorded"}</span>{verdict.txHash && verdict.txHash !== "dev-mode" && <code>{shortId(verdict.txHash)}</code>}</div>
    </div>
  );
}

function Icon({ name }) {
  const paths = {
    grid: <><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/></>,
    layers: <><path d="m12 3 9 5-9 5-9-5 9-5Z"/><path d="m3 12 9 5 9-5M3 16l9 5 9-5"/></>,
    shield: <><path d="M12 22s8-4 8-11V5l-8-3-8 3v6c0 7 8 11 8 11Z"/><path d="m9 12 2 2 4-4"/></>,
    spark: <><path d="m12 3 1.9 5.8L20 11l-6.1 2.2L12 19l-2-5.8L4 11l6-2.2L12 3Z"/><path d="m19 14 1 2.5 2.5 1-2.5 1L19 21l-1-2.5-2.5-1 2.5-1L19 14Z"/></>,
    check: <><path d="m5 12 4 4L19 6"/><circle cx="12" cy="12" r="9"/></>,
    alert: <><path d="M10.3 3.9 2.7 17a2 2 0 0 0 1.7 3h15.2a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z"/><path d="M12 9v4m0 4h.01"/></>,
    lock: <><rect x="4" y="10" width="16" height="11" rx="2"/><path d="M8 10V7a4 4 0 1 1 8 0v3m-4 5v2"/></>,
    fingerprint: <><path d="M12 11a2 2 0 0 0-2 2c0 2.2-.3 4.4-1 6.5M14 13c0 3.2-.4 5.8-1.1 8M16 14c-.1 2.5-.4 4.6-.9 6.3M18 16c-.1 1.4-.3 2.7-.6 3.8M7 15.5c-.2-1.1-.3-2.4-.3-3.7a5.3 5.3 0 0 1 10.6 0M5 12c0-4 3-7 7-7s7 3 7 7M3.5 9.5A8.8 8.8 0 0 1 12 3"/></>,
  };
  return <svg className={`icon icon-${name}`} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[name] || paths.spark}</svg>;
}

export default App;
