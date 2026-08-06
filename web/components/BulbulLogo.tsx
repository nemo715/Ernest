"use client";

// Ernest wordmark mark: a minimal illustrated red-whiskered bulbul
// (Pycnonotus jocosus) in side profile — the tall crest, the pale cheek
// patch and the red whisker below the eye are the three field marks.
//
// The silhouette is a single filled shape (currentColor), the cheek is
// punched out with the page background (var(--bg)) so the mark adapts to
// light and dark surfaces, and the whisker is the brand accent red.
import { CSSProperties } from "react";

export function BulbulLogo({
  size = 28,
  style,
}: {
  size?: number;
  style?: CSSProperties;
}) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      fill="none"
      aria-hidden
      style={style}
    >
      {/* body + head + beak silhouette */}
      <path
        fill="currentColor"
        d="M52.5 24
           Q48.4 23.2 45.5 23.7
           C44.8 17.9 40.4 14.3 35 14.5
           C33.6 14.6 32.4 15.1 31.4 16
           C26.8 17.1 21 21.1 17.4 27.6
           C13.8 34.1 14 41.6 18.4 46.4
           C21.6 49.8 27.2 51.9 33.2 51.4
           C38.8 50.9 43 47.7 45.4 43.7
           C47.6 39.9 47.1 33.5 45.6 30.7
           C47.4 29.5 48.5 27.7 48.8 25.7
           Z"
      />
      {/* crest */}
      <path
        fill="currentColor"
        d="M31.8 15.3
           C30.6 9.7 27.4 5.9 22.8 4.7
           C21.9 5.7 22.5 7.5 23.7 8.5
           C25.6 10.3 26.8 12.5 27.3 14.5
           Z"
      />
      {/* tail */}
      <path
        fill="currentColor"
        d="M19.2 43.7
           Q12.3 47.1 6.6 51.6
           Q13.2 51.2 19.7 50.6
           Q19.6 46.9 19.2 43.7
           Z"
      />
      {/* pale cheek patch */}
      <circle cx="34.9" cy="21.9" r="2.7" fill="var(--bg)" />
      {/* the red whisker — the signature field mark */}
      <path
        fill="#ff5c6c"
        d="M33 23.8
           C30.7 25.6 29.8 28.2 30.4 30.3
           C30.6 31.1 31.3 31.5 32.1 31.2
           C31.3 29.7 31.7 27.7 33.3 26.1
           Z"
      />
    </svg>
  );
}
