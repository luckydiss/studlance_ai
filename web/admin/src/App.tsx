import { BrowserRouter, Navigate, Outlet, Route, Routes } from "react-router-dom";
import { AdminAuth } from "./AdminAuth";
import { AdminHeader } from "./components/AdminHeader";
import { ClientsPage } from "./pages/ClientsPage";
import { JobReviewPage } from "./pages/JobReviewPage";
import { JobsPage } from "./pages/JobsPage";
import { WorkersPage } from "./pages/WorkersPage";

// Admin panel routes (08-web-admin.md): /admin → /admin/jobs, plus the job
// review, clients and workers screens. Deep links work after a reload.
// Everything private sits behind the role gate.

function AdminLayout() {
  return (
    <div className="sl-admin-app">
      <AdminHeader />
      <Outlet />
    </div>
  );
}

export function App() {
  return (
    <BrowserRouter basename="/admin">
      <Routes>
        <Route element={<AdminAuth layout={AdminLayout} />}>
          <Route path="/" element={<Navigate to="/jobs" replace />} />
          <Route path="/jobs" element={<JobsPage />} />
          <Route path="/jobs/:id" element={<JobReviewPage />} />
          <Route path="/clients" element={<ClientsPage />} />
          <Route path="/workers" element={<WorkersPage />} />
          <Route path="*" element={<Navigate to="/jobs" replace />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}
