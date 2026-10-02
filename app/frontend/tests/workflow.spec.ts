import { test, expect, type Page } from "@playwright/test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

async function connect(page: Page, project: string) {
  await page.addInitScript((project) => {
    const methods = [
      "ListContracts",
      "SaveDraft",
      "GetDraft",
      "PublishContract",
      "ListVersions",
      "GetVersion",
      "StartRun",
      "EvaluateAttempt",
      "GetRun",
      "ListRuns",
    ];
    const api: Record<string, (...args: unknown[]) => Promise<unknown>> = {};
    for (const method of methods)
      api[method] = async (...args) => {
        const response = await fetch("http://127.0.0.1:5174/rpc", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ method, args }),
        });
        const payload = await response.json();
        if (payload.error) throw new Error(payload.error);
        return payload.result;
      };
    api.SelectProject = async () => project;
    window.go = {
      main: {
        Desktop: api as unknown as NonNullable<Window["go"]>["main"]["Desktop"],
      },
    };
  }, project);
  await page.goto("/");
  await expect(
    page.getByRole("button", { name: "＋ New draft" }),
  ).toBeEnabled();
}

test("missing file fails, a correction adds a pass, and history survives reload", async ({
  page,
}) => {
  const project = await mkdtemp(join(tmpdir(), "harness-browser-"));
  try {
    await connect(page, project);
    await page.getByRole("button", { name: "＋ New draft" }).click();
    await page
      .getByLabel("Contract name", { exact: true })
      .fill("History demo");
    await page.getByRole("button", { name: "Publish version" }).click();
    await expect(
      page.getByText("Published version 1. Its rules are now frozen."),
    ).toBeVisible();
    await page.getByRole("button", { name: "Browse", exact: true }).click();
    await expect(page.getByLabel("Project directory")).toHaveValue(project);
    await page.getByRole("button", { name: "Run checks", exact: true }).click();
    await expect(page.getByText("Attempt 1", { exact: true })).toBeVisible();
    await expect(
      page.getByText("README.md does not exist", { exact: true }),
    ).toBeVisible();
    await writeFile(join(project, "README.md"), "Corrected");
    await page
      .getByRole("button", { name: "Recheck project", exact: true })
      .click();
    await expect(page.getByText("Attempt 2", { exact: true })).toBeVisible();
    await expect(page.getByText("File exists.", { exact: true })).toBeVisible();
    await expect(
      page.getByText("README.md does not exist", { exact: true }),
    ).toBeVisible();
    await page.reload();
    await page
      .getByRole("button")
      .filter({ hasText: "History demo" })
      .filter({ hasText: project })
      .click();
    await expect(page.getByText("Attempt 1", { exact: true })).toBeVisible();
    await expect(page.getByText("Attempt 2", { exact: true })).toBeVisible();
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: "test-results/history.png", fullPage: true });
  } finally {
    await rm(project, { recursive: true, force: true });
  }
});

test("invalid dependencies cannot publish, and published rules stay frozen", async ({
  page,
}) => {
  await connect(page, "");
  await page.getByRole("button", { name: "＋ New draft" }).click();
  await page.getByLabel("Contract name", { exact: true }).fill("Version demo");
  await page.getByLabel("Dependencies 1", { exact: true }).fill("missing");
  await page.getByRole("button", { name: "Publish version" }).click();
  await expect(page.getByRole("alert")).toContainText("does not exist");
  await expect(page.getByLabel("Run version")).toHaveValue("0");
  await page.getByLabel("Dependencies 1", { exact: true }).fill("");
  await page.getByRole("button", { name: "Publish version" }).click();
  await expect(
    page.getByText("Published version 1. Its rules are now frozen."),
  ).toBeVisible();
  await expect(page.getByLabel("File path 1", { exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Edit draft" }).click();
  await page.getByLabel("File path 1", { exact: true }).fill("NEW.md");
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  await page.getByLabel("Contract view").selectOption("1");
  await expect(page.getByLabel("File path 1", { exact: true })).toHaveValue(
    "README.md",
  );
  await expect(page.getByLabel("File path 1", { exact: true })).toBeDisabled();
});

test("plain browser preview explains that the desktop connection is required", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.getByRole("alert")).toContainText(
    "Open the harness desktop app",
  );
  await page.getByRole("button", { name: "Create first draft" }).click();
  await expect(
    page.getByRole("button", { name: "Save draft", exact: true }),
  ).toBeDisabled();
});
