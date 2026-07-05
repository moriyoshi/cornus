/* @refresh reload */
import { render } from "solid-js/web";
import { Router, Route } from "@solidjs/router";
import App from "./App";
import Overview from "./views/Overview";
import WorkloadDetail from "./views/WorkloadDetail";
import Metrics from "./views/Metrics";
import Workspace from "./views/Workspace";
import Settings from "./views/Settings";
import "./styles.css";

render(
  () => (
    <Router root={App}>
      <Route path="/" component={Overview} />
      <Route path="/workloads/:name" component={WorkloadDetail} />
      <Route path="/metrics" component={Metrics} />
      <Route path="/workspace" component={Workspace} />
      <Route path="/settings" component={Settings} />
    </Router>
  ),
  document.getElementById("root")!,
);
