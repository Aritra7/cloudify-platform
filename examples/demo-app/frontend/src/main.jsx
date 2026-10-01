import React from "react";
import { createRoot } from "react-dom/client";
import { getHealth } from "./api";

function App() {
  const [status, setStatus] = React.useState("not checked");
  return (
    <main>
      <h1>Cloudify controlled demo</h1>
      <p>Backend status: {status}</p>
      <button onClick={() => getHealth().then((result) => setStatus(result.status))}>
        Check backend
      </button>
    </main>
  );
}

createRoot(document.getElementById("root")).render(<App />);
