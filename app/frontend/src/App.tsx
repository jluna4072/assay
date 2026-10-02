import { useEffect, useMemo, useState } from "react";
import {
  Background,
  Controls,
  MarkerType,
  Position,
  ReactFlow,
  type Edge,
  type Node,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { desktop } from "./bridge";
import type { Contract, PublishedContract, Rule, Run } from "./types";

type EditorRule = Rule & { key: string; dependenciesText: string };
const editRules = (rules: Rule[] | null): EditorRule[] =>
  (rules ?? []).map((rule) => ({
    ...rule,
    key: crypto.randomUUID(),
    dependenciesText: (rule.dependsOn ?? []).join(", "),
  }));
const statusLabels: Record<string, string> = {
  passed: "Passed",
  failed: "Failed",
  error: "Operational error",
  blocked: "Blocked",
  ready: "Ready",
};
const statusLabel = (status: string) => statusLabels[status] ?? status;
const dateLabel = (value: string) => new Date(value).toLocaleString();

function Graph({ rules }: { rules: Rule[] }) {
  const { nodes, edges } = useMemo(() => {
    const index = new Map(rules.map((rule, i) => [rule.id, i]));
    const depth = (i: number, seen = new Set<number>()): number => {
      if (seen.has(i)) return 0;
      const next = new Set(seen).add(i);
      return Math.max(
        0,
        ...(rules[i].dependsOn ?? [])
          .filter((id) => index.has(id))
          .map((id) => 1 + depth(index.get(id)!, next)),
      );
    };
    const columns = new Map<number, number>();
    const nodes: Node[] = rules.map((rule, i) => {
      const column = Math.min(depth(i), rules.length);
      const row = columns.get(column) ?? 0;
      columns.set(column, row + 1);
      return {
        id: String(i),
        position: { x: column * 245, y: row * 120 },
        data: {
          label: (
            <div>
              <strong>{rule.id || "Unnamed rule"}</strong>
              <span>{rule.path || "Set a file path"}</span>
            </div>
          ),
        },
        style: { width: 200 },
        sourcePosition: Position.Right,
        targetPosition: Position.Left,
      };
    });
    const edges: Edge[] = rules.flatMap((rule, i) =>
      (rule.dependsOn ?? [])
        .filter((id) => index.has(id))
        .map((id, j) => ({
          id: `${i}-${j}`,
          source: String(index.get(id)),
          target: String(i),
          type: "smoothstep",
          markerEnd: { type: MarkerType.ArrowClosed },
        })),
    );
    return { nodes, edges };
  }, [rules]);
  return (
    <div className="graph" aria-label="Rule dependency graph">
      <ReactFlow
        key={JSON.stringify(rules.map((r) => [r.id, r.dependsOn]))}
        nodes={nodes}
        edges={edges}
        fitView
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        minZoom={0.25}
      >
        <Background gap={20} color="#d4ddd8" />
        <Controls showInteractive={false} />
      </ReactFlow>
      {rules.length === 0 && (
        <p className="graph-empty">Add a rule to preview its dependencies.</p>
      )}
    </div>
  );
}

export default function App() {
  const [contracts, setContracts] = useState<Contract[]>([]);
  const [draft, setDraft] = useState<Contract | null>(null);
  const [rules, setRules] = useState<EditorRule[]>([]);
  const [versions, setVersions] = useState<PublishedContract[]>([]);
  const [viewVersion, setViewVersion] = useState(0);
  const [runVersion, setRunVersion] = useState(0);
  const [project, setProject] = useState("");
  const [run, setRun] = useState<Run | null>(null);
  const [history, setHistory] = useState<Run[]>([]);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const connected = Boolean(window.go?.main.Desktop);
  const serializedRules: Rule[] = rules.map(
    ({ id, criterion, kind, path, dependenciesText }) => ({
      id,
      criterion,
      kind,
      path,
      dependsOn: dependenciesText
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
    }),
  );
  const viewed = viewVersion
    ? versions.find((v) => v.version === viewVersion)?.contract
    : draft;
  const displayRules = viewVersion ? editRules(viewed?.rules ?? []) : rules;
  const previewRules = viewVersion ? (viewed?.rules ?? []) : serializedRules;
  const locked = viewVersion !== 0;

  async function perform(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await action();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }
  function assignDraft(c: Contract) {
    setDraft(c);
    setRules(editRules(c.rules));
    setDirty(false);
  }
  async function loadContract(id: string) {
    const [c, published] = await Promise.all([
      desktop().GetDraft(id),
      desktop().ListVersions(id),
    ]);
    assignDraft(c);
    setVersions(published);
    setViewVersion(0);
    setRunVersion(published.at(-1)?.version ?? 0);
    setRun(null);
  }
  function canLeave() {
    return !dirty || window.confirm("Discard unsaved draft changes?");
  }
  useEffect(() => {
    void perform(async () => {
      const [items, runs] = await Promise.all([
        desktop().ListContracts(),
        desktop().ListRuns(),
      ]);
      setContracts(items);
      setHistory(runs);
      if (items.length) await loadContract(items[0].id);
    });
  }, []);

  function newDraft() {
    if (!canLeave()) return;
    assignDraft({
      id: crypto.randomUUID(),
      name: "New contract",
      rules: [
        {
          id: "readme",
          criterion: "Project contains a README",
          kind: "file_exists",
          path: "README.md",
          dependsOn: [],
        },
      ],
    });
    setVersions([]);
    setViewVersion(0);
    setRunVersion(0);
    setRun(null);
    setDirty(true);
    setError("");
    setNotice("");
  }
  function changeRule(
    key: string,
    field: "id" | "criterion" | "path" | "dependenciesText",
    value: string,
  ) {
    setRules((current) =>
      current.map((rule) =>
        rule.key === key ? { ...rule, [field]: value } : rule,
      ),
    );
    setDirty(true);
  }
  async function saveDraft() {
    if (!draft) return;
    const saved = await desktop().SaveDraft({
      ...draft,
      rules: serializedRules,
    });
    assignDraft(saved);
    setContracts(await desktop().ListContracts());
  }
  async function publish() {
    if (!draft) return;
    await saveDraft();
    const published = await desktop().PublishContract(draft.id);
    setVersions(await desktop().ListVersions(draft.id));
    setRunVersion(published.version);
    setViewVersion(published.version);
    setRun(null);
    setNotice(
      `Published version ${published.version}. Its rules are now frozen.`,
    );
  }
  async function check(newRun = false) {
    if (!draft) return;
    let active = run;
    if (
      newRun ||
      !active ||
      active.contractId !== draft.id ||
      active.version !== runVersion ||
      active.projectDir !== project
    ) {
      active = await desktop().StartRun(draft.id, runVersion, project);
      setRun(active);
      setProject(active.projectDir);
    }
    const updated = await desktop().EvaluateAttempt(active.id);
    setRun(updated);
    setHistory(await desktop().ListRuns());
  }
  async function openRun(summary: Run) {
    const selected = await desktop().GetRun(summary.id);
    await loadContract(selected.contractId);
    setRun(selected);
    setProject(selected.projectDir);
    setRunVersion(selected.version);
    setViewVersion(selected.version);
  }
  const frozenRules =
    versions.find((v) => v.version === run?.version)?.contract.rules ?? [];

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark">h.</span>
          <div>
            harness<small>LOCAL WORKSPACE</small>
          </div>
        </div>
        <div className="sidebar-heading">
          Contracts <span>{contracts.length}</span>
        </div>
        <button className="new-contract" onClick={newDraft} disabled={busy}>
          ＋ New draft
        </button>
        <nav aria-label="Contracts">
          {contracts.map((c) => (
            <button
              className={`contract-link ${draft?.id === c.id ? "active" : ""}`}
              key={c.id}
              onClick={() => {
                if (canLeave()) void perform(() => loadContract(c.id));
              }}
              disabled={busy}
            >
              <span className="contract-icon">◇</span>
              <span>{c.name || "Untitled contract"}</span>
            </button>
          ))}
        </nav>
        {contracts.length === 0 && (
          <p className="sidebar-empty">
            Your first contract starts with one clear acceptance rule.
          </p>
        )}
        <div className="sidebar-footer">
          <span className="connection-dot" />
          Local checks<small>Published rules. Visible decisions.</small>
        </div>
      </aside>
      <main>
        <header className="page-header">
          <div>
            <div className="eyebrow">CONTRACT WORKSPACE</div>
            <h1>{draft?.name || "Make acceptance clear."}</h1>
            <p>
              Create rules, publish a version, and check a project against it.
            </p>
          </div>
          <span className="local-badge">● Local only</span>
        </header>
        <div aria-live="polite">
          {error && (
            <div className="message error" role="alert">
              {error}
            </div>
          )}
          {notice && <div className="message success">{notice}</div>}
          {busy && <div className="message working">Working…</div>}
        </div>
        {!draft ? (
          <section className="welcome card">
            <span className="welcome-icon">◇</span>
            <h2>Start with a contract</h2>
            <p>
              Describe the files your project should contain. Publish the rules
              when you’re ready, then keep a record of every check.
            </p>
            <button className="primary" onClick={newDraft} disabled={busy}>
              Create first draft
            </button>
          </section>
        ) : (
          <>
            <div className="workspace-grid">
              <section className="card editor">
                <div className="section-heading">
                  <div>
                    <span className="eyebrow">01 / DEFINE</span>
                    <h2>
                      {locked
                        ? `Published version ${viewVersion}`
                        : "Contract draft"}
                    </h2>
                  </div>
                  <span className={`pill ${locked ? "published" : "draft"}`}>
                    {locked ? "Frozen" : dirty ? "Unsaved draft" : "Draft"}
                  </span>
                </div>
                <div className="view-switch">
                  <label>
                    View
                    <select
                      aria-label="Contract view"
                      value={viewVersion}
                      disabled={busy}
                      onChange={(e) => {
                        const target = Number(e.target.value);
                        if (canLeave())
                          void perform(async () => {
                            if (dirty && draft) await loadContract(draft.id);
                            setViewVersion(target);
                          });
                      }}
                    >
                      <option value={0}>Editable draft</option>
                      {versions.map((v) => (
                        <option key={v.version} value={v.version}>
                          Published version {v.version}
                        </option>
                      ))}
                    </select>
                  </label>
                  {locked && (
                    <button onClick={() => setViewVersion(0)} disabled={busy}>
                      Edit draft
                    </button>
                  )}
                </div>
                <label>
                  Contract name
                  <input
                    aria-label="Contract name"
                    value={viewed?.name ?? ""}
                    disabled={locked || busy}
                    onChange={(e) => {
                      setDraft({ ...draft, name: e.target.value });
                      setDirty(true);
                    }}
                  />
                </label>
                <div className="rules-heading">
                  <h3>Acceptance rules</h3>
                  <span>
                    {displayRules.length}{" "}
                    {displayRules.length === 1 ? "rule" : "rules"}
                  </span>
                </div>
                {displayRules.map((rule, i) => (
                  <div className="rule-form" key={locked ? i : rule.key}>
                    <div className="rule-top">
                      <span className="rule-number">
                        {String(i + 1).padStart(2, "0")}
                      </span>
                      <span>File exists</span>
                      {!locked && (
                        <button
                          className="text-button"
                          aria-label={`Remove rule ${i + 1}`}
                          onClick={() => {
                            setRules(rules.filter((r) => r.key !== rule.key));
                            setDirty(true);
                          }}
                          disabled={busy}
                        >
                          Remove
                        </button>
                      )}
                    </div>
                    <label>
                      Rule ID
                      <input
                        aria-label={`Rule ID ${i + 1}`}
                        value={rule.id}
                        disabled={locked || busy}
                        onChange={(e) =>
                          changeRule(rule.key, "id", e.target.value)
                        }
                      />
                    </label>
                    <label>
                      Acceptance criterion
                      <input
                        aria-label={`Acceptance criterion ${i + 1}`}
                        value={rule.criterion}
                        disabled={locked || busy}
                        onChange={(e) =>
                          changeRule(rule.key, "criterion", e.target.value)
                        }
                      />
                    </label>
                    <label>
                      File path
                      <input
                        aria-label={`File path ${i + 1}`}
                        placeholder="README.md"
                        value={rule.path}
                        disabled={locked || busy}
                        onChange={(e) =>
                          changeRule(rule.key, "path", e.target.value)
                        }
                      />
                    </label>
                    <label>
                      Depends on
                      <input
                        aria-label={`Dependencies ${i + 1}`}
                        placeholder="Rule IDs, separated by commas"
                        value={rule.dependenciesText}
                        disabled={locked || busy}
                        onChange={(e) =>
                          changeRule(
                            rule.key,
                            "dependenciesText",
                            e.target.value,
                          )
                        }
                      />
                    </label>
                  </div>
                ))}
                {!locked && (
                  <>
                    <button
                      className="add-rule"
                      disabled={busy}
                      onClick={() => {
                        setRules([
                          ...rules,
                          ...editRules([
                            {
                              id: `rule-${rules.length + 1}`,
                              criterion: "",
                              kind: "file_exists",
                              path: "",
                              dependsOn: [],
                            },
                          ]),
                        ]);
                        setDirty(true);
                      }}
                    >
                      ＋ Add rule
                    </button>
                    <div className="editor-actions">
                      <button
                        disabled={busy || !connected}
                        onClick={() =>
                          void perform(async () => {
                            await saveDraft();
                            setNotice("Draft saved.");
                          })
                        }
                      >
                        Save draft
                      </button>
                      <button
                        className="primary"
                        disabled={busy || !connected}
                        onClick={() => void perform(publish)}
                      >
                        Publish version
                      </button>
                    </div>
                    <p className="hint">
                      Publishing validates the rules and freezes a version. You
                      can keep editing a separate draft.
                    </p>
                  </>
                )}
              </section>
              <div className="right-column">
                <section className="card graph-card">
                  <div className="section-heading">
                    <div>
                      <span className="eyebrow">02 / CONNECT</span>
                      <h2>Dependency preview</h2>
                    </div>
                    <span className="subtle">
                      {locked ? `Version ${viewVersion}` : "Draft"}
                    </span>
                  </div>
                  <Graph rules={previewRules} />
                  <p className="hint">
                    An arrow means the dependent rule needs its prerequisite to
                    pass.
                  </p>
                </section>
                <section className="card run-card">
                  <div className="section-heading">
                    <div>
                      <span className="eyebrow">03 / CHECK</span>
                      <h2>Check a project</h2>
                    </div>
                    {run && (
                      <span className={`pill ${run.status}`}>
                        {statusLabel(run.status)}
                      </span>
                    )}
                  </div>
                  <label>
                    Published version
                    <select
                      aria-label="Run version"
                      value={runVersion}
                      disabled={busy}
                      onChange={(e) => {
                        setRunVersion(Number(e.target.value));
                        setRun(null);
                      }}
                    >
                      <option value={0}>Publish a version first</option>
                      {versions.map((v) => (
                        <option key={v.version} value={v.version}>
                          Version {v.version}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label>
                    Project directory
                    <div className="project-input">
                      <input
                        aria-label="Project directory"
                        placeholder="Choose a project folder"
                        value={project}
                        disabled={busy}
                        onChange={(e) => setProject(e.target.value)}
                      />
                      <button
                        disabled={busy || !connected}
                        onClick={() =>
                          void perform(async () => {
                            const path = await desktop().SelectProject();
                            if (path) setProject(path);
                          })
                        }
                      >
                        Browse
                      </button>
                    </div>
                  </label>
                  <div className="run-actions">
                    <button
                      className="primary"
                      disabled={
                        busy || !connected || !runVersion || !project.trim()
                      }
                      onClick={() => void perform(() => check())}
                    >
                      {run ? "Recheck project" : "Run checks"}
                    </button>
                    {run && (
                      <button
                        disabled={busy || !project.trim()}
                        onClick={() => void perform(() => check(true))}
                      >
                        Start new run
                      </button>
                    )}
                  </div>
                  <p className="hint">
                    Checks use the selected published version. Rechecks add
                    attempts to the same run.
                  </p>
                </section>
              </div>
            </div>
            <section className="card attempts">
              <div className="section-heading">
                <div>
                  <span className="eyebrow">04 / UNDERSTAND</span>
                  <h2>Attempt history</h2>
                </div>
                {run && (
                  <span className="subtle">
                    Version {run.version} · {run.attempts.length} attempts
                  </span>
                )}
              </div>
              {!run || run.attempts.length === 0 ? (
                <p className="empty-state">
                  Run checks to see each rule’s result and failure reason here.
                </p>
              ) : (
                <>
                  <p className="run-project">{run.projectDir}</p>
                  {[...run.attempts].reverse().map((attempt) => (
                    <details className="attempt" key={attempt.number} open>
                      <summary>
                        <strong>Attempt {attempt.number}</strong>
                        <time>{dateLabel(attempt.completedAt)}</time>
                        <span className={`pill ${attempt.status}`}>
                          {statusLabel(attempt.status)}
                        </span>
                      </summary>
                      <div className="results">
                        {attempt.results.map((result) => (
                          <div className="result-row" key={result.ruleId}>
                            <span className={`result-dot ${result.status}`} />
                            <div>
                              <strong>{result.ruleId}</strong>
                              <small>
                                {
                                  frozenRules.find(
                                    (rule) => rule.id === result.ruleId,
                                  )?.criterion
                                }
                              </small>
                              <p>
                                {result.reason ||
                                  (result.passed
                                    ? "File exists."
                                    : "No reason recorded.")}
                              </p>
                            </div>
                            <span className={`pill ${result.status}`}>
                              {statusLabel(result.status)}
                            </span>
                          </div>
                        ))}
                      </div>
                    </details>
                  ))}
                </>
              )}
            </section>
          </>
        )}
        <section className="saved-runs">
          <div className="section-heading">
            <h2>Saved runs</h2>
            <span className="subtle">{history.length} total</span>
          </div>
          {history.length === 0 ? (
            <p className="hint">
              Runs and their history will remain available after you close the
              app.
            </p>
          ) : (
            history.map((item) => (
              <button
                className={`saved-run ${run?.id === item.id ? "selected" : ""}`}
                key={item.id}
                disabled={busy}
                onClick={() => {
                  if (canLeave()) void perform(() => openRun(item));
                }}
              >
                <span>
                  <strong>
                    {contracts.find((c) => c.id === item.contractId)?.name ||
                      item.contractId}
                  </strong>
                  <small>{item.projectDir}</small>
                </span>
                <span className="subtle">Version {item.version}</span>
                <span className={`pill ${item.status}`}>
                  {statusLabel(item.status)}
                </span>
              </button>
            ))
          )}
        </section>
      </main>
    </div>
  );
}
