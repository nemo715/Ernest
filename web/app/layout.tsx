import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "ernest playground",
  description: "Streaming chat UI for ernest agents (multi-agent framework).",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
