import styles from "./home/Home.module.css";
import { HowCarousel } from "./home/HowCarousel";
import { KitSection } from "./home/KitSection";
import { OrderForm } from "./home/OrderForm";

export function HomePage() {
  return (
    <main className={styles.home}>
      <OrderForm />
      <KitSection />
      <HowCarousel />
    </main>
  );
}
