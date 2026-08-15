// Auto-connects the google-services MCP server on session start.
// Zero friction: user installs the package, restarts Pi, tools are ready.
//
// Self-healing: if npm's allowScripts blocked the postinstall, the binary may
// be missing — this extension installs it on first session start.
import { existsSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const BIN_NAME = "pi-google-services";
const BIN_PATH = path.join(os.homedir(), ".local", "bin", BIN_NAME);
const INSTALLER_PATH = fileURLToPath(new URL("../install.js", import.meta.url));

const MCP_RECONNECT = "/mcp reconnect google-services";

async function ensureBinary(pi: ExtensionAPI): Promise<boolean> {
	if (existsSync(BIN_PATH)) return true;
	try {
		const res = await pi.exec(process.execPath, [INSTALLER_PATH], {
			timeout: 60_000,
		});
		return res.code === 0 && existsSync(BIN_PATH);
	} catch {
		return false;
	}
}

export default function (pi: ExtensionAPI) {
	pi.on("session_start", async () => {
		// /mcp is provided by pi-mcp-adapter, not Pi core. Detect it and guide
		// the user instead of sending a command that doesn't exist.
		const hasMcp = pi.getCommands().some((cmd) => cmd.name === "mcp");
		if (!hasMcp) {
			pi.sendUserMessage(
				"⚠️ pi-google-services: el comando /mcp no está disponible. " +
					"Instalá pi-mcp-adapter (`pi install npm:pi-mcp-adapter`) y reiniciá la sesión.",
				{ deliverAs: "followUp" },
			);
			return;
		}

		// npm postinstall can be blocked by Pi (allowScripts). Install on demand.
		const installed = await ensureBinary(pi);
		if (!installed) {
			pi.sendUserMessage(
				"⚠️ pi-google-services: no se pudo instalar el binario. " +
					`Corré manualmente: node ${INSTALLER_PATH}`,
				{ deliverAs: "followUp" },
			);
			return;
		}

		// expandPromptTemplates dispatches /mcp as an extension command instead
		// of sending it to the model as plain text.
		pi.sendUserMessage(MCP_RECONNECT, {
			deliverAs: "followUp",
			expandPromptTemplates: true,
		});
	});
}
