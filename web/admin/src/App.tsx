import { BrowserRouter, Route, Routes } from "react-router-dom";
import { AdminHeader } from "./components/AdminHeader";
import { JobsPage } from "./pages/JobsPage";

// Skeleton router for the admin panel (full screens arrive in PR 6).
export function App() {
  return (
    <BrowserRouter basename="/admin">
      <AdminHeader />
      <Routes>
        <Route path="/" element={<JobsPage />} />
        <Route path="/jobs" element={<JobsPage />} />
        <Route path="*" element={<JobsPage />} />
      </Routes>
    </BrowserRouter>
  );
}
