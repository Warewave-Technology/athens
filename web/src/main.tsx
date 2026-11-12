import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError } from "./api/client";
import { App } from "./App";
import { LoginPage } from "./features/login/LoginPage";
import "./styles/theme.css";
import "./styles/app.css";
import "./styles/login.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
  },
});

// /login renders without a session, so it must not call the API.
const isLogin = window.location.pathname === "/login";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    {isLogin ? (
      <LoginPage />
    ) : (
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    )}
  </StrictMode>,
);
