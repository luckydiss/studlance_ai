import { apiErrorMessage, useCurrentUser, useLogout, useToast } from "@studlance/shared";
import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

// 56px white header (Main.dc.html): studlance brand, «Мои заказы», and the
// initials circle with a logout menu.

function initials(name: string, email: string): string {
  const source = name.trim() || email;
  const parts = source.split(/\s+/).filter(Boolean);
  if (parts.length === 0) {
    return "…";
  }
  const first = parts[0]?.[0] ?? "";
  const second = parts.length > 1 ? (parts[1]?.[0] ?? "") : (parts[0]?.[1] ?? "");
  return (first + second).toUpperCase();
}

export function Header() {
  const { data: user } = useCurrentUser();
  const logout = useLogout();
  const navigate = useNavigate();
  const toast = useToast();
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
      toast.show(apiErrorMessage(err));
    }
    // The query cache is cleared by useLogout; go to the login page.
    navigate("/login", { replace: true });
  };

  return (
    <header className="sl-header">
      <div className="sl-header-left">
        <Link to="/" className="sl-brand">
          studlance
        </Link>
        <nav aria-label="Основная навигация">
          <Link to="/orders" className="sl-header-link">
            Мои заказы
          </Link>
        </nav>
      </div>
      <div className="sl-header-user" ref={menuRef}>
        <button
          type="button"
          className="sl-avatar"
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          aria-label={`Меню пользователя ${user?.name ?? user?.email ?? ""}`}
          onClick={() => setMenuOpen((v) => !v)}
        >
          {initials(user?.name ?? "", user?.email ?? "")}
        </button>
        {menuOpen && (
          <div className="sl-user-menu" role="menu">
            <div className="sl-user-menu-email">{user?.email}</div>
            <button type="button" role="menuitem" className="sl-user-menu-item" onClick={onLogout}>
              Выйти
            </button>
          </div>
        )}
      </div>
    </header>
  );
}
