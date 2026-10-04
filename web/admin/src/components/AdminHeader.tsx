import { Link } from "react-router-dom";

export function AdminHeader() {
  return (
    <header className="sl-header sl-header--dark">
      <Link to="/" className="sl-brand">
        studlance · пульт
      </Link>
      <nav>
        <Link to="/jobs">Заказы</Link>
      </nav>
    </header>
  );
}
