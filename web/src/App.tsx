import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Link, Navigate, Route, Routes } from "react-router-dom";
import { ApiError } from "./api/client";
import { AuthProvider } from "./auth/AuthProvider";
import { ToastProvider } from "./components/Toast";
import { Shell } from "./components/Shell";
import { EmptyPanel } from "./components/PageBits";
import { DashboardPage } from "./pages/DashboardPage";
import { NewItemPage } from "./pages/NewItemPage";
import { JobPage, JobStatusView } from "./pages/JobPage";
import { JobConfigView } from "./pages/JobConfigView";
import { JobDependenciesView } from "./pages/JobDependenciesView";
import { RunPage } from "./pages/RunPage";
import { ExecutorsPage } from "./pages/ExecutorsPage";
import { ControlPlanePage } from "./pages/ControlPlanePage";
import { ExecutorPage } from "./pages/ExecutorPage";
import { MasterDeployPage } from "./pages/MasterDeployPage";
import { WorkerSetupPage } from "./pages/WorkerSetupPage";
import { QueuePage } from "./pages/QueuePage";
import { HistoryPage } from "./pages/HistoryPage";
import { ApiPage } from "./pages/ApiPage";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error) =>
        error instanceof ApiError && error.status >= 500 && failureCount < 2,
      refetchOnWindowFocus: false,
      staleTime: 3000,
    },
    mutations: {
      retry: false,
    },
  },
});

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <ToastProvider>
          <BrowserRouter basename="/ui">
            <Routes>
              <Route element={<Shell />}>
                <Route index element={<DashboardPage />} />
                <Route path="new" element={<NewItemPage />} />
                <Route path="job/:jobId" element={<JobPage />}>
                  <Route index element={<JobStatusView />} />
                  <Route path="config" element={<JobConfigView />} />
                  <Route path="dependencies" element={<JobDependenciesView />} />
                </Route>
                <Route path="run/:runId" element={<RunPage />} />
                <Route path="queue" element={<QueuePage />} />
                <Route path="history" element={<HistoryPage />} />
                <Route path="executors" element={<ExecutorsPage />} />
                <Route path="executors/setup" element={<WorkerSetupPage />} />
                <Route path="deploy" element={<MasterDeployPage />} />
                <Route path="control-plane" element={<ControlPlanePage />} />
                <Route path="executors/:executorId" element={<ExecutorPage />} />
                <Route path="workers" element={<Navigate to="/executors" replace />} />
                <Route path="api" element={<ApiPage />} />
                <Route
                  path="*"
                  element={
                    <EmptyPanel
                      title="页面不存在"
                      message="控制台中不存在该页面。"
                      action={
                        <Link className="btn primary" to="/">
                          返回任务列表
                        </Link>
                      }
                    />
                  }
                />
              </Route>
            </Routes>
          </BrowserRouter>
        </ToastProvider>
      </AuthProvider>
    </QueryClientProvider>
  );
}
