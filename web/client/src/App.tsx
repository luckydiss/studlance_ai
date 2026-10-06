import { BrowserRouter, Outlet, Route, Routes } from "react-router-dom";
import { RequireAuth } from "./RequireAuth";
import { Header } from "./components/Header";
import { HomePage } from "./pages/HomePage";
import { JobPage } from "./pages/JobPage";
import { LoginPage } from "./pages/LoginPage";
import { OrdersPage } from "./pages/OrdersPage";

function AuthedLayout() {
  return (
    <div className="sl-app">
      <Header />
      <Outlet />
    </div>
  );
}

export function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route element={<RequireAuth layout={AuthedLayout} />}>
          <Route path="/" element={<HomePage />} />
          <Route path="/orders" element={<OrdersPage />} />
          <Route path="/orders/:id" element={<JobPage />} />
          <Route path="*" element={<HomePage />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}
