import { apiErrorMessage, useCurrentUser, useLogout, useToast } from "@studlance/shared";
import { useEffect, useRef, useState } from "react";
import { Link, NavLink } from "react-router-dom";

// Dark admin header (08-web-admin.md): brand «studlance · пульт», the three
// panel sections, the account email and «Выйти». The panel never looks like
// the cabinet.

export function AdminHeader() {
  const { data: user } = useCurrentUser();
  const logout = useLogout();
  const { show } = useToast();
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!menuOpen) {
      return;
    }
    const onDocClick = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    };
    const onEsc = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setMenuOpen(false);
      }
    };
    document.addEventListener("mousedown", onDocClick);
    document.addEventListener("keydown", onEsc);
    return () => {
      document.removeEventListener("mousedown", onDocClick);
      document.removeEventListener("keydown", onEsc);
    };
  }, [menuOpen]);

  const onLogout = async () => {
    setMenuOpen(false);
    try {
      await logout.mutateAsync();
    } catch (err) {
      show(apiErrorMessage(err));
    }
    // The login form lives in the cabinet SPA: a full navigation is required.
    window.location.replace("/login");
  };

  const linkClass = ({ isActive }: { isActive: boolean }) =>
    `sl-admin-link${isActive ? " sl-admin-link--active" : ""}`;

  return (
    <header className="sl-admin-header">
      <div className="sl-admin-header-left">
        <Link to="/jobs" className="sl-admin-brand">
          studlance <span>· пульт</span>
        </Link>
        <nav aria-label="Навигация пульта">
          <NavLink to="/jobs" className={linkClass}>
            Заказы
          </NavLink>
          <NavLink to="/clients" className={linkClass}>
            Клиенты
          </NavLink>
          <NavLink to="/workers" className={linkClass}>
            Воркеры
          </NavLink>
        </nav>
      </div>
      <div className="sl-admin-user" ref={menuRef}>
        <span className="sl-admin-email">{user?.email}</span>
        <button
          type="button"
          className="sl-admin-logout"
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          onClick={() => setMenuOpen((v) => !v)}
        >
          Выйти
        </button>
        {menuOpen && (
          <div className="sl-admin-menu" role="menu">
            <div className="sl-admin-menu-email">{user?.email}</div>
            <button type="button" role="menuitem" className="sl-admin-menu-item" onClick={onLogout}>
              Выйти
            </button>
          </div>
        )}
      </div>
    </header>
  );
}
