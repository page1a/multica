import localFont from "next/font/local";
import { LocaleProvider } from "@/features/landing/i18n";
import { getRequestLocale } from "@/lib/request-locale";

// Instrument Serif is the landing display face and is Latin-only. The full
// `--font-serif` stack (Instrument Serif + the per-locale CJK serif tail) is
// composed in static CSS in app/custom.css, not here — same reasoning as
// `--font-sans` in app/globals.css: the CJK tail must be overridable per
// `<html lang>`, and a hashed next/font family can only be referenced from CSS
// through its variable.
//
// File is a latin-subset woff2 in apps/web/fonts so self-host Docker builds do
// not fetch Google Fonts at compile time. Weight 400 matches the previous
// `next/font/google` Instrument_Serif entry.
const instrumentSerif = localFont({
  src: "../../fonts/instrument-serif-latin-400-normal.woff2",
  weight: "400",
  display: "swap",
  variable: "--font-instrument-serif",
});

const jsonLd = {
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "Organization",
      name: "Multica",
      url: "https://www.multica.ai",
      sameAs: ["https://github.com/multica-ai/multica"],
    },
    {
      "@type": "SoftwareApplication",
      name: "Multica",
      applicationCategory: "ProjectManagement",
      operatingSystem: "Web",
      description:
        "Source-available project management platform that turns coding agents into real teammates.",
      offers: {
        "@type": "Offer",
        price: "0",
        priceCurrency: "USD",
      },
    },
  ],
};

export default async function LandingLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const initialLocale = await getRequestLocale();

  return (
    <>
      <script
        type="application/ld+json"
        dangerouslySetInnerHTML={{ __html: JSON.stringify(jsonLd) }}
      />
      <div className={`${instrumentSerif.variable} landing-light h-full overflow-x-hidden overflow-y-auto bg-white`}>
        <LocaleProvider initialLocale={initialLocale}>{children}</LocaleProvider>
      </div>
    </>
  );
}
