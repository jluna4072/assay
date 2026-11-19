import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// Vite empties dist/ on every build. Go's embed of frontend/dist needs the
// directory to exist in a fresh checkout, so restore the tracked placeholder.
function keepDistPlaceholder(): Plugin {
  return {
    name: "keep-dist-placeholder",
    apply: "build",
    closeBundle() {
      writeFileSync(fileURLToPath(new URL("dist/.gitkeep", import.meta.url)), "");
    },
  };
}

export default defineConfig({
  plugins: [react(), keepDistPlaceholder()],
  server: { port: 5173, strictPort: true },
});
