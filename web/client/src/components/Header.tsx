import { Link } from "react-router-dom";

export function Header() {
  return (
    <header className="sl-header">
      <Link to="/" className="sl-brand">
        studlance
      </Link>
      <nav>
        <Link to="/orders">Мои заказы</Link>
      </nav>
    </header>
  );
}
