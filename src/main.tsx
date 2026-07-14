import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import Home from "../app/page";
import "../app/globals.css";
import "../app/desktop.css";

const root = document.getElementById("root");

if (!root) {
  throw new Error("Dashboard root element not found");
}

createRoot(root).render(
  <StrictMode>
    <Home />
  </StrictMode>,
);
