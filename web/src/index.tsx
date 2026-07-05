/* @refresh reload */
import { render } from "solid-js/web";
import { Router, Route } from "@solidjs/router";
import App from "./App";
import Overview from "./views/Overview";
import WorkloadDetail from "./views/WorkloadDetail";
import Files from "./views/Files";
import Terminal from "./views/Terminal";
import Settings from "./views/Settings";
import "./styles.css";

render(
  () => (
    <Router root={App}>
      <Route path="/" component={Overview} />
      <Route path="/workloads/:name" component={WorkloadDetail} />
      <Route path="/files" component={Files} />
      <Route path="/terminal" component={Terminal} />
      <Route path="/settings" component={Settings} />
    </Router>
  ),
  document.getElementById("root")!,
);
