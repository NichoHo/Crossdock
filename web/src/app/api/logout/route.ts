import { NextRequest, NextResponse } from "next/server";
import { endSession } from "@/lib/session";

export async function POST(req: NextRequest) {
  await endSession();
  return NextResponse.redirect(new URL("/login", req.url), 303);
}
