const ROW_BACKUP_KEY = "rowset.editor.backupRows";

// Saving rows before UPDATE/DELETE is on unless the user turned it off.
export function rowBackupEnabled() {
  return localStorage.getItem(ROW_BACKUP_KEY) !== "off";
}

export function setRowBackupEnabled(on: boolean) {
  localStorage.setItem(ROW_BACKUP_KEY, on ? "on" : "off");
}

const GRID_DENSITY_KEY = "rowset.editor.gridDensity";

// A result grid shows as many columns as it can fit unless the user asks for
// the roomier rows.
export function gridDensity(): "compact" | "comfortable" {
  return localStorage.getItem(GRID_DENSITY_KEY) === "comfortable" ? "comfortable" : "compact";
}

export function setGridDensity(density: "compact" | "comfortable") {
  localStorage.setItem(GRID_DENSITY_KEY, density);
}
