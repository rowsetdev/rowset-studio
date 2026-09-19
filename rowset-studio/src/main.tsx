import { mountStudio } from "./studio";
import "./index.css";

// A browser tab can outlive a desktop-app update. If it then opens a lazy
// route, its old bundle asks the new server for a chunk that no longer exists.
// Vite exposes that exact failure so we can load the new index and asset set
// automatically instead of leaving the URL on the new route with stale UI.
window.addEventListener("vite:preloadError", (event) => {
  event.preventDefault();
  window.location.reload();
});

mountStudio(document.getElementById("root")!);
