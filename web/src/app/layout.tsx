import type { Metadata, Viewport } from "next";
import { Atkinson_Hyperlegible, JetBrains_Mono } from "next/font/google";
import "./globals.css";

const atkinson = Atkinson_Hyperlegible({ weight: ["400", "700"], subsets: ["latin"], variable: "--font-atkinson", display: "swap" });
const jetbrains = JetBrains_Mono({ weight: ["400", "700"], subsets: ["latin"], variable: "--font-jetbrains", display: "swap" });

export const metadata: Metadata = {
  title: { default: "Crossdock", template: "%s | Crossdock" },
  description: "Warehouse floor console and scanner.",
  manifest: "/manifest.webmanifest",
};
export const viewport: Viewport = { themeColor: "#0b5c8e", width: "device-width", initialScale: 1 };

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`${atkinson.variable} ${jetbrains.variable}`}>
      <body>{children}</body>
    </html>
  );
}
